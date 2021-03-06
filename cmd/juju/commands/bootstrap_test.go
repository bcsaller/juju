// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package commands

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/juju/cmd"
	"github.com/juju/errors"
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	"github.com/juju/utils/arch"
	jujuos "github.com/juju/utils/os"
	"github.com/juju/utils/series"
	"github.com/juju/version"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/cloud"
	"github.com/juju/juju/cmd/modelcmd"
	cmdtesting "github.com/juju/juju/cmd/testing"
	"github.com/juju/juju/constraints"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/bootstrap"
	"github.com/juju/juju/environs/filestorage"
	"github.com/juju/juju/environs/gui"
	"github.com/juju/juju/environs/imagemetadata"
	"github.com/juju/juju/environs/simplestreams"
	sstesting "github.com/juju/juju/environs/simplestreams/testing"
	"github.com/juju/juju/environs/sync"
	envtesting "github.com/juju/juju/environs/testing"
	envtools "github.com/juju/juju/environs/tools"
	toolstesting "github.com/juju/juju/environs/tools/testing"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/juju/keys"
	"github.com/juju/juju/juju/osenv"
	"github.com/juju/juju/jujuclient"
	"github.com/juju/juju/jujuclient/jujuclienttesting"
	"github.com/juju/juju/network"
	"github.com/juju/juju/provider/dummy"
	coretesting "github.com/juju/juju/testing"
	coretools "github.com/juju/juju/tools"
	jujuversion "github.com/juju/juju/version"
)

type BootstrapSuite struct {
	coretesting.FakeJujuXDGDataHomeSuite
	testing.MgoSuite
	envtesting.ToolsFixture
	store *jujuclienttesting.MemStore
}

var _ = gc.Suite(&BootstrapSuite{})

func init() {
	dummyProvider, err := environs.Provider("dummy")
	if err != nil {
		panic(err)
	}
	environs.RegisterProvider("no-cloud-region-detection", noCloudRegionDetectionProvider{})
	environs.RegisterProvider("no-cloud-regions", noCloudRegionsProvider{dummyProvider})
	environs.RegisterProvider("no-credentials", noCredentialsProvider{})
	environs.RegisterProvider("many-credentials", manyCredentialsProvider{dummyProvider})
}

func (s *BootstrapSuite) SetUpSuite(c *gc.C) {
	s.FakeJujuXDGDataHomeSuite.SetUpSuite(c)
	s.MgoSuite.SetUpSuite(c)
	s.PatchValue(&keys.JujuPublicKey, sstesting.SignedMetadataPublicKey)
}

func (s *BootstrapSuite) SetUpTest(c *gc.C) {
	s.FakeJujuXDGDataHomeSuite.SetUpTest(c)
	s.MgoSuite.SetUpTest(c)
	s.ToolsFixture.SetUpTest(c)

	// Set jujuversion.Current to a known value, for which we
	// will make tools available. Individual tests may
	// override this.
	s.PatchValue(&jujuversion.Current, v100p64.Number)
	s.PatchValue(&arch.HostArch, func() string { return v100p64.Arch })
	s.PatchValue(&series.HostSeries, func() string { return v100p64.Series })
	s.PatchValue(&jujuos.HostOS, func() jujuos.OSType { return jujuos.Ubuntu })

	// Set up a local source with tools.
	sourceDir := createToolsSource(c, vAll)
	s.PatchValue(&envtools.DefaultBaseURL, sourceDir)

	s.PatchValue(&envtools.BundleTools, toolstesting.GetMockBundleTools(c))

	s.PatchValue(&waitForAgentInitialisation, func(*cmd.Context, *modelcmd.ModelCommandBase, string) error {
		return nil
	})

	// TODO(wallyworld) - add test data when tests are improved
	s.store = jujuclienttesting.NewMemStore()
}

func (s *BootstrapSuite) TearDownSuite(c *gc.C) {
	s.MgoSuite.TearDownSuite(c)
	s.FakeJujuXDGDataHomeSuite.TearDownSuite(c)
}

func (s *BootstrapSuite) TearDownTest(c *gc.C) {
	s.ToolsFixture.TearDownTest(c)
	s.MgoSuite.TearDownTest(c)
	s.FakeJujuXDGDataHomeSuite.TearDownTest(c)
	dummy.Reset(c)
}

func (s *BootstrapSuite) newBootstrapCommand() cmd.Command {
	c := &bootstrapCommand{}
	c.SetClientStore(s.store)
	return modelcmd.Wrap(c)
}

func (s *BootstrapSuite) TestRunTests(c *gc.C) {
	for i, test := range bootstrapTests {
		c.Logf("\ntest %d: %s", i, test.info)
		restore := s.run(c, test)
		restore()
	}
}

type bootstrapTest struct {
	info string
	// binary version string used to set jujuversion.Current
	version string
	sync    bool
	args    []string
	err     string
	// binary version string for expected tools; if set, no default tools
	// will be uploaded before running the test.
	upload               string
	constraints          constraints.Value
	bootstrapConstraints constraints.Value
	placement            string
	hostArch             string
	keepBroken           bool
}

func (s *BootstrapSuite) patchVersionAndSeries(c *gc.C, hostSeries string) {
	resetJujuXDGDataHome(c)
	s.PatchValue(&series.HostSeries, func() string { return hostSeries })
	s.patchVersion(c)
}

func (s *BootstrapSuite) patchVersion(c *gc.C) {
	// Force a dev version by having a non zero build number.
	// This is because we have not uploaded any tools and auto
	// upload is only enabled for dev versions.
	num := jujuversion.Current
	num.Build = 1234
	s.PatchValue(&jujuversion.Current, num)
}

func (s *BootstrapSuite) run(c *gc.C, test bootstrapTest) testing.Restorer {
	// Create home with dummy provider and remove all
	// of its envtools.
	resetJujuXDGDataHome(c)
	dummy.Reset(c)

	var restore testing.Restorer = func() {
		s.store = jujuclienttesting.NewMemStore()
	}
	if test.version != "" {
		useVersion := strings.Replace(test.version, "%LTS%", series.LatestLts(), 1)
		v := version.MustParseBinary(useVersion)
		restore = restore.Add(testing.PatchValue(&jujuversion.Current, v.Number))
		restore = restore.Add(testing.PatchValue(&arch.HostArch, func() string { return v.Arch }))
		restore = restore.Add(testing.PatchValue(&series.HostSeries, func() string { return v.Series }))
	}

	if test.hostArch != "" {
		restore = restore.Add(testing.PatchValue(&arch.HostArch, func() string { return test.hostArch }))
	}

	controllerName := "peckham-controller"
	cloudName := "dummy"

	// Run command and check for uploads.
	args := append([]string{
		controllerName, cloudName,
		"--config", "default-series=raring",
	}, test.args...)
	opc, errc := cmdtesting.RunCommand(cmdtesting.NullContext(c), s.newBootstrapCommand(), args...)
	// Check for remaining operations/errors.
	if test.err != "" {
		err := <-errc
		c.Assert(err, gc.NotNil)
		stripped := strings.Replace(err.Error(), "\n", "", -1)
		c.Check(stripped, gc.Matches, test.err)
		return restore
	}
	if !c.Check(<-errc, gc.IsNil) {
		return restore
	}

	op, ok := <-opc
	c.Assert(ok, gc.Equals, true)
	opBootstrap := op.(dummy.OpBootstrap)
	c.Check(opBootstrap.Env, gc.Equals, bootstrap.ControllerModelName)
	c.Check(opBootstrap.Args.ModelConstraints, gc.DeepEquals, test.constraints)
	if test.bootstrapConstraints == (constraints.Value{}) {
		test.bootstrapConstraints = test.constraints
	}
	c.Check(opBootstrap.Args.BootstrapConstraints, gc.DeepEquals, test.bootstrapConstraints)
	c.Check(opBootstrap.Args.Placement, gc.Equals, test.placement)

	opFinalizeBootstrap := (<-opc).(dummy.OpFinalizeBootstrap)
	c.Check(opFinalizeBootstrap.Env, gc.Equals, bootstrap.ControllerModelName)
	c.Check(opFinalizeBootstrap.InstanceConfig.ToolsList(), gc.Not(gc.HasLen), 0)
	if test.upload != "" {
		c.Check(opFinalizeBootstrap.InstanceConfig.AgentVersion().String(), gc.Equals, test.upload)
	}

	// Check controllers.yaml controller details.
	addrConnectedTo := []string{"localhost:17070"}

	controller, err := s.store.ControllerByName(controllerName)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(controller.CACert, gc.Not(gc.Equals), "")
	c.Assert(controller.UnresolvedAPIEndpoints, gc.DeepEquals, addrConnectedTo)
	c.Assert(controller.APIEndpoints, gc.DeepEquals, addrConnectedTo)
	c.Assert(utils.IsValidUUIDString(controller.ControllerUUID), jc.IsTrue)

	controllerModel, err := s.store.ModelByName(controllerName, "admin@local", bootstrap.ControllerModelName)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(controllerModel.ModelUUID, gc.Equals, controller.ControllerUUID)

	// Bootstrap config should have been saved, and should only contain
	// the type, name, and any user-supplied configuration.
	bootstrapConfig, err := s.store.BootstrapConfigForController(controllerName)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(bootstrapConfig.Cloud, gc.Equals, "dummy")
	c.Assert(bootstrapConfig.Credential, gc.Equals, "")
	c.Assert(bootstrapConfig.Config, jc.DeepEquals, map[string]interface{}{
		"name":            bootstrap.ControllerModelName,
		"type":            "dummy",
		"default-series":  "raring",
		"authorized-keys": "public auth key\n",
	})

	return restore
}

var bootstrapTests = []bootstrapTest{{
	info: "no args, no error, no upload, no constraints",
}, {
	info: "bad --constraints",
	args: []string{"--constraints", "bad=wrong"},
	err:  `invalid value "bad=wrong" for flag --constraints: unknown constraint "bad"`,
}, {
	info: "conflicting --constraints",
	args: []string{"--constraints", "instance-type=foo mem=4G"},
	err:  `ambiguous constraints: "instance-type" overlaps with "mem"`,
}, {
	info:    "bad model",
	version: "1.2.3-%LTS%-amd64",
	args:    []string{"--config", "broken=Bootstrap Destroy", "--auto-upgrade"},
	err:     `failed to bootstrap model: dummy.Bootstrap is broken`,
}, {
	info:        "constraints",
	args:        []string{"--constraints", "mem=4G cpu-cores=4"},
	constraints: constraints.MustParse("mem=4G cpu-cores=4"),
}, {
	info:                 "bootstrap and environ constraints",
	args:                 []string{"--constraints", "mem=4G cpu-cores=4", "--bootstrap-constraints", "mem=8G"},
	constraints:          constraints.MustParse("mem=4G cpu-cores=4"),
	bootstrapConstraints: constraints.MustParse("mem=8G cpu-cores=4"),
}, {
	info:        "unsupported constraint passed through but no error",
	args:        []string{"--constraints", "mem=4G cpu-cores=4 cpu-power=10"},
	constraints: constraints.MustParse("mem=4G cpu-cores=4 cpu-power=10"),
}, {
	info:        "--upload-tools uses arch from constraint if it matches current version",
	version:     "1.3.3-saucy-ppc64el",
	hostArch:    "ppc64el",
	args:        []string{"--upload-tools", "--constraints", "arch=ppc64el"},
	upload:      "1.3.3.1-raring-ppc64el", // from jujuversion.Current
	constraints: constraints.MustParse("arch=ppc64el"),
}, {
	info:     "--upload-tools rejects mismatched arch",
	version:  "1.3.3-saucy-amd64",
	hostArch: "amd64",
	args:     []string{"--upload-tools", "--constraints", "arch=ppc64el"},
	err:      `failed to bootstrap model: cannot build tools for "ppc64el" using a machine running on "amd64"`,
}, {
	info:     "--upload-tools rejects non-supported arch",
	version:  "1.3.3-saucy-mips64",
	hostArch: "mips64",
	args:     []string{"--upload-tools"},
	err:      fmt.Sprintf(`failed to bootstrap model: model %q of type dummy does not support instances running on "mips64"`, bootstrap.ControllerModelName),
}, {
	info:     "--upload-tools always bumps build number",
	version:  "1.2.3.4-raring-amd64",
	hostArch: "amd64",
	args:     []string{"--upload-tools"},
	upload:   "1.2.3.5-raring-amd64",
}, {
	info:      "placement",
	args:      []string{"--to", "something"},
	placement: "something",
}, {
	info:       "keep broken",
	args:       []string{"--keep-broken"},
	keepBroken: true,
}, {
	info: "additional args",
	args: []string{"anything", "else"},
	err:  `unrecognized args: \["anything" "else"\]`,
}, {
	info: "--agent-version with --upload-tools",
	args: []string{"--agent-version", "1.1.0", "--upload-tools"},
	err:  `--agent-version and --upload-tools can't be used together`,
}, {
	info: "invalid --agent-version value",
	args: []string{"--agent-version", "foo"},
	err:  `invalid version "foo"`,
}, {
	info:    "agent-version doesn't match client version major",
	version: "1.3.3-saucy-ppc64el",
	args:    []string{"--agent-version", "2.3.0"},
	err:     `requested agent version major.minor mismatch`,
}, {
	info:    "agent-version doesn't match client version minor",
	version: "1.3.3-saucy-ppc64el",
	args:    []string{"--agent-version", "1.4.0"},
	err:     `requested agent version major.minor mismatch`,
}, {
	info: "--clouds with --regions",
	args: []string{"--clouds", "--regions", "aws"},
	err:  `--clouds and --regions can't be used together`,
}}

func (s *BootstrapSuite) TestRunControllerNameMissing(c *gc.C) {
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand())
	c.Check(err, gc.ErrorMatches, "controller name and cloud name are required")
}

func (s *BootstrapSuite) TestRunCloudNameMissing(c *gc.C) {
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "my-controller")
	c.Check(err, gc.ErrorMatches, "controller name and cloud name are required")
}

func (s *BootstrapSuite) TestRunCloudNameUnknown(c *gc.C) {
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "my-controller", "unknown")
	c.Check(err, gc.ErrorMatches, `unknown cloud "unknown", please try "juju update-clouds"`)
}

func (s *BootstrapSuite) TestCheckProviderProvisional(c *gc.C) {
	err := checkProviderType("devcontroller")
	c.Assert(err, jc.ErrorIsNil)

	for name, flag := range provisionalProviders {
		// vsphere is disabled for gccgo. See lp:1440940.
		if name == "vsphere" && runtime.Compiler == "gccgo" {
			continue
		}
		c.Logf(" - trying %q -", name)
		err := checkProviderType(name)
		c.Check(err, gc.ErrorMatches, ".* provider is provisional .* set JUJU_DEV_FEATURE_FLAGS=.*")

		err = os.Setenv(osenv.JujuFeatureFlagEnvKey, flag)
		c.Assert(err, jc.ErrorIsNil)
		err = checkProviderType(name)
		c.Check(err, jc.ErrorIsNil)
	}
}

func (s *BootstrapSuite) TestBootstrapTwice(c *gc.C) {
	const controllerName = "dev"
	s.patchVersionAndSeries(c, "raring")

	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), controllerName, "dummy", "--auto-upgrade")
	c.Assert(err, jc.ErrorIsNil)

	_, err = coretesting.RunCommand(c, s.newBootstrapCommand(), controllerName, "dummy", "--auto-upgrade")
	c.Assert(err, gc.ErrorMatches, `controller "dev" already exists`)
}

func (s *BootstrapSuite) TestBootstrapSetsCurrentModel(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "devcontroller", "dummy", "--auto-upgrade")
	c.Assert(err, jc.ErrorIsNil)
	currentController := s.store.CurrentControllerName
	c.Assert(currentController, gc.Equals, "devcontroller")
	modelName, err := s.store.CurrentModel(currentController, "admin@local")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(modelName, gc.Equals, "default")
}

func (s *BootstrapSuite) TestBootstrapDefaultModel(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"devcontroller", "dummy",
		"--auto-upgrade",
		"--default-model", "mymodel",
		"--config", "foo=bar",
	)
	c.Assert(utils.IsValidUUIDString(bootstrap.args.ControllerConfig.ControllerUUID()), jc.IsTrue)
	c.Assert(bootstrap.args.HostedModelConfig["name"], gc.Equals, "mymodel")
	c.Assert(bootstrap.args.HostedModelConfig["foo"], gc.Equals, "bar")
}

func (s *BootstrapSuite) TestBootstrapTimeout(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})
	coretesting.RunCommand(
		c, s.newBootstrapCommand(), "devcontroller", "dummy", "--auto-upgrade",
		"--config", "bootstrap-timeout=99",
	)
	c.Assert(bootstrap.args.DialOpts.Timeout, gc.Equals, 99*time.Second)
}

func (s *BootstrapSuite) TestBootstrapDefaultConfigStripsProcessedAttributes(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	fakeSSHFile := filepath.Join(c.MkDir(), "ssh")
	err := ioutil.WriteFile(fakeSSHFile, []byte("ssh-key"), 0600)
	c.Assert(err, jc.ErrorIsNil)
	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"devcontroller", "dummy",
		"--auto-upgrade",
		"--config", "authorized-keys-path="+fakeSSHFile,
	)
	_, ok := bootstrap.args.HostedModelConfig["authorized-keys-path"]
	c.Assert(ok, jc.IsFalse)
}

func (s *BootstrapSuite) TestBootstrapDefaultConfigStripsInheritedAttributes(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	fakeSSHFile := filepath.Join(c.MkDir(), "ssh")
	err := ioutil.WriteFile(fakeSSHFile, []byte("ssh-key"), 0600)
	c.Assert(err, jc.ErrorIsNil)
	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"devcontroller", "dummy",
		"--auto-upgrade",
		"--config", "authorized-keys=ssh-key",
		"--config", "agent-version=1.19.0",
	)
	_, ok := bootstrap.args.HostedModelConfig["authorized-keys"]
	c.Assert(ok, jc.IsFalse)
	_, ok = bootstrap.args.HostedModelConfig["agent-version"]
	c.Assert(ok, jc.IsFalse)
}

func (s *BootstrapSuite) TestBootstrapWithGUI(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	var bootstrap fakeBootstrapFuncs

	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})
	coretesting.RunCommand(c, s.newBootstrapCommand(), "devcontroller", "dummy")
	c.Assert(bootstrap.args.GUIDataSourceBaseURL, gc.Equals, gui.DefaultBaseURL)
}

func (s *BootstrapSuite) TestBootstrapWithCustomizedGUI(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	s.PatchEnvironment("JUJU_GUI_SIMPLESTREAMS_URL", "https://1.2.3.4/gui/streams")

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	coretesting.RunCommand(c, s.newBootstrapCommand(), "devcontroller", "dummy")
	c.Assert(bootstrap.args.GUIDataSourceBaseURL, gc.Equals, "https://1.2.3.4/gui/streams")
}

func (s *BootstrapSuite) TestBootstrapWithoutGUI(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	var bootstrap fakeBootstrapFuncs

	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})
	coretesting.RunCommand(c, s.newBootstrapCommand(), "devcontroller", "dummy", "--no-gui")
	c.Assert(bootstrap.args.GUIDataSourceBaseURL, gc.Equals, "")
}

type mockBootstrapInstance struct {
	instance.Instance
}

func (*mockBootstrapInstance) Addresses() ([]network.Address, error) {
	return []network.Address{{Value: "localhost"}}, nil
}

// In the case where we cannot examine the client store, we want the
// error to propagate back up to the user.
func (s *BootstrapSuite) TestBootstrapPropagatesStoreErrors(c *gc.C) {
	const controllerName = "devcontroller"
	s.patchVersionAndSeries(c, "raring")

	store := jujuclienttesting.NewStubStore()
	store.SetErrors(errors.New("oh noes"))
	cmd := &bootstrapCommand{}
	cmd.SetClientStore(store)
	wrapped := modelcmd.Wrap(cmd, modelcmd.ModelSkipFlags, modelcmd.ModelSkipDefault)
	_, err := coretesting.RunCommand(c, wrapped, controllerName, "dummy", "--auto-upgrade")
	store.CheckCallNames(c, "CredentialForCloud")
	c.Assert(err, gc.ErrorMatches, `loading credentials: oh noes`)
}

// When attempting to bootstrap, check that when prepare errors out,
// bootstrap will stop immediately. Nothing will be destroyed.
func (s *BootstrapSuite) TestBootstrapFailToPrepareDiesGracefully(c *gc.C) {
	destroyed := false
	s.PatchValue(&environsDestroy, func(name string, _ environs.Environ, _ jujuclient.ControllerStore) error {
		c.Assert(name, gc.Equals, "decontroller")
		destroyed = true
		return nil
	})

	s.PatchValue(&bootstrapPrepare, func(
		environs.BootstrapContext,
		jujuclient.ClientStore,
		bootstrap.PrepareParams,
	) (environs.Environ, error) {
		return nil, fmt.Errorf("mock-prepare")
	})

	ctx := coretesting.Context(c)
	_, errc := cmdtesting.RunCommand(
		ctx, s.newBootstrapCommand(),
		"devcontroller", "dummy",
	)
	c.Check(<-errc, gc.ErrorMatches, ".*mock-prepare$")
	c.Check(destroyed, jc.IsFalse)
}

func (s *BootstrapSuite) writeControllerModelAccountInfo(c *gc.C, controller, model, account string) {
	err := s.store.UpdateController(controller, jujuclient.ControllerDetails{
		CACert:         "x",
		ControllerUUID: "y",
	})
	c.Assert(err, jc.ErrorIsNil)
	err = s.store.SetCurrentController(controller)
	c.Assert(err, jc.ErrorIsNil)
	err = s.store.UpdateAccount(controller, account, jujuclient.AccountDetails{
		User:     account,
		Password: "secret",
	})
	c.Assert(err, jc.ErrorIsNil)
	err = s.store.SetCurrentAccount(controller, account)
	c.Assert(err, jc.ErrorIsNil)
	err = s.store.UpdateModel(controller, account, model, jujuclient.ModelDetails{
		ModelUUID: "model-uuid",
	})
	c.Assert(err, jc.ErrorIsNil)
	err = s.store.SetCurrentModel(controller, account, model)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *BootstrapSuite) TestBootstrapErrorRestoresOldMetadata(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	s.PatchValue(&bootstrapPrepare, func(
		environs.BootstrapContext,
		jujuclient.ClientStore,
		bootstrap.PrepareParams,
	) (environs.Environ, error) {
		s.writeControllerModelAccountInfo(c, "foo", "bar", "foobar@local")
		return nil, fmt.Errorf("mock-prepare")
	})

	s.writeControllerModelAccountInfo(c, "olddevcontroller", "fredmodel", "fred@local")
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "devcontroller", "dummy", "--auto-upgrade")
	c.Assert(err, gc.ErrorMatches, "mock-prepare")

	oldCurrentController := s.store.CurrentControllerName
	c.Assert(oldCurrentController, gc.Equals, "olddevcontroller")
	oldCurrentAccount, err := s.store.CurrentAccount(oldCurrentController)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(oldCurrentAccount, gc.Equals, "fred@local")
	oldCurrentModel, err := s.store.CurrentModel(oldCurrentController, oldCurrentAccount)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(oldCurrentModel, gc.Equals, "fredmodel")
}

func (s *BootstrapSuite) TestBootstrapAlreadyExists(c *gc.C) {
	const controllerName = "devcontroller"
	s.patchVersionAndSeries(c, "raring")

	s.writeControllerModelAccountInfo(c, "devcontroller", "fredmodel", "fred@local")

	ctx := coretesting.Context(c)
	_, errc := cmdtesting.RunCommand(ctx, s.newBootstrapCommand(), controllerName, "dummy", "--auto-upgrade")
	err := <-errc
	c.Assert(err, jc.Satisfies, errors.IsAlreadyExists)
	c.Assert(err, gc.ErrorMatches, fmt.Sprintf(`controller %q already exists`, controllerName))
	currentController := s.store.CurrentControllerName
	c.Assert(currentController, gc.Equals, "devcontroller")
	currentAccount, err := s.store.CurrentAccount(currentController)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(currentAccount, gc.Equals, "fred@local")
	currentModel, err := s.store.CurrentModel(currentController, currentAccount)
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(currentModel, gc.Equals, "fredmodel")
}

func (s *BootstrapSuite) TestInvalidLocalSource(c *gc.C) {
	s.PatchValue(&jujuversion.Current, version.MustParse("1.2.0"))
	resetJujuXDGDataHome(c)

	// Bootstrap the controller with an invalid source.
	// The command returns with an error.
	_, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "--metadata-source", c.MkDir(),
		"devcontroller", "dummy",
	)
	c.Check(err, gc.ErrorMatches, `failed to bootstrap model: Juju cannot bootstrap because no tools are available for your model(.|\n)*`)
}

// createImageMetadata creates some image metadata in a local directory.
func createImageMetadata(c *gc.C) (string, []*imagemetadata.ImageMetadata) {
	// Generate some image metadata.
	im := []*imagemetadata.ImageMetadata{
		{
			Id:         "1234",
			Arch:       "amd64",
			Version:    "13.04",
			RegionName: "region",
			Endpoint:   "endpoint",
		},
	}
	cloudSpec := &simplestreams.CloudSpec{
		Region:   "region",
		Endpoint: "endpoint",
	}
	sourceDir := c.MkDir()
	sourceStor, err := filestorage.NewFileStorageWriter(sourceDir)
	c.Assert(err, jc.ErrorIsNil)
	err = imagemetadata.MergeAndWriteMetadata("raring", im, cloudSpec, sourceStor)
	c.Assert(err, jc.ErrorIsNil)
	return sourceDir, im
}

func (s *BootstrapSuite) TestBootstrapCalledWithMetadataDir(c *gc.C) {
	sourceDir, _ := createImageMetadata(c)
	resetJujuXDGDataHome(c)

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"--metadata-source", sourceDir, "--constraints", "mem=4G",
		"devcontroller", "dummy-cloud/region-1",
		"--config", "default-series=raring",
	)
	c.Assert(bootstrap.args.MetadataDir, gc.Equals, sourceDir)
}

func (s *BootstrapSuite) checkBootstrapWithVersion(c *gc.C, vers, expect string) {
	resetJujuXDGDataHome(c)

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	num := jujuversion.Current
	num.Major = 2
	num.Minor = 3
	s.PatchValue(&jujuversion.Current, num)
	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"--agent-version", vers,
		"devcontroller", "dummy-cloud/region-1",
		"--config", "default-series=raring",
	)
	c.Assert(bootstrap.args.AgentVersion, gc.NotNil)
	c.Assert(*bootstrap.args.AgentVersion, gc.Equals, version.MustParse(expect))
}

func (s *BootstrapSuite) TestBootstrapWithVersionNumber(c *gc.C) {
	s.checkBootstrapWithVersion(c, "2.3.4", "2.3.4")
}

func (s *BootstrapSuite) TestBootstrapWithBinaryVersionNumber(c *gc.C) {
	s.checkBootstrapWithVersion(c, "2.3.4-trusty-ppc64", "2.3.4")
}

func (s *BootstrapSuite) TestBootstrapWithAutoUpgrade(c *gc.C) {
	resetJujuXDGDataHome(c)

	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})
	coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"--auto-upgrade",
		"devcontroller", "dummy-cloud/region-1",
	)
	c.Assert(bootstrap.args.AgentVersion, gc.IsNil)
}

func (s *BootstrapSuite) TestAutoSyncLocalSource(c *gc.C) {
	sourceDir := createToolsSource(c, vAll)
	s.PatchValue(&jujuversion.Current, version.MustParse("1.2.0"))
	series.SetLatestLtsForTesting("trusty")
	resetJujuXDGDataHome(c)

	// Bootstrap the controller with the valid source.
	// The bootstrapping has to show no error, because the tools
	// are automatically synchronized.
	_, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "--metadata-source", sourceDir,
		"devcontroller", "dummy-cloud/region-1",
	)
	c.Assert(err, jc.ErrorIsNil)

	p, err := environs.Provider("dummy")
	c.Assert(err, jc.ErrorIsNil)
	cfg, err := modelcmd.NewGetBootstrapConfigFunc(s.store)("devcontroller")
	c.Assert(err, jc.ErrorIsNil)
	env, err := p.PrepareForBootstrap(envtesting.BootstrapContext(c), cfg)
	c.Assert(err, jc.ErrorIsNil)

	// Now check the available tools which are the 1.2.0 envtools.
	checkTools(c, env, v120All)
}

func (s *BootstrapSuite) setupAutoUploadTest(c *gc.C, vers, ser string) {
	s.PatchValue(&envtools.BundleTools, toolstesting.GetMockBundleTools(c))
	sourceDir := createToolsSource(c, vAll)
	s.PatchValue(&envtools.DefaultBaseURL, sourceDir)

	// Change the tools location to be the test location and also
	// the version and ensure their later restoring.
	// Set the current version to be something for which there are no tools
	// so we can test that an upload is forced.
	s.PatchValue(&jujuversion.Current, version.MustParse(vers))
	s.PatchValue(&series.HostSeries, func() string { return ser })

	// Create home with dummy provider and remove all
	// of its envtools.
	resetJujuXDGDataHome(c)
}

func (s *BootstrapSuite) TestAutoUploadAfterFailedSync(c *gc.C) {
	s.PatchValue(&series.HostSeries, func() string { return series.LatestLts() })
	s.setupAutoUploadTest(c, "1.7.3", "quantal")
	// Run command and check for that upload has been run for tools matching
	// the current juju version.
	opc, errc := cmdtesting.RunCommand(
		cmdtesting.NullContext(c), s.newBootstrapCommand(),
		"devcontroller", "dummy-cloud/region-1",
		"--config", "default-series=raring",
		"--auto-upgrade",
	)
	c.Assert(<-errc, gc.IsNil)
	c.Check((<-opc).(dummy.OpBootstrap).Env, gc.Equals, bootstrap.ControllerModelName)
	icfg := (<-opc).(dummy.OpFinalizeBootstrap).InstanceConfig
	c.Assert(icfg, gc.NotNil)
	c.Assert(icfg.AgentVersion().String(), gc.Equals, "1.7.3.1-raring-"+arch.HostArch())
}

func (s *BootstrapSuite) TestAutoUploadOnlyForDev(c *gc.C) {
	s.setupAutoUploadTest(c, "1.8.3", "precise")
	_, errc := cmdtesting.RunCommand(
		cmdtesting.NullContext(c), s.newBootstrapCommand(),
		"devcontroller", "dummy-cloud/region-1",
	)
	err := <-errc
	c.Assert(err, gc.ErrorMatches,
		"failed to bootstrap model: Juju cannot bootstrap because no tools are available for your model(.|\n)*")
}

func (s *BootstrapSuite) TestMissingToolsError(c *gc.C) {
	s.setupAutoUploadTest(c, "1.8.3", "precise")

	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(),
		"devcontroller", "dummy-cloud/region-1",
		"--config", "default-series=raring",
	)
	c.Assert(err, gc.ErrorMatches,
		"failed to bootstrap model: Juju cannot bootstrap because no tools are available for your model(.|\n)*")
}

func (s *BootstrapSuite) TestMissingToolsUploadFailedError(c *gc.C) {

	buildToolsTarballAlwaysFails := func(forceVersion *version.Number, stream string) (*sync.BuiltTools, error) {
		return nil, fmt.Errorf("an error")
	}

	s.setupAutoUploadTest(c, "1.7.3", "precise")
	s.PatchValue(&sync.BuildToolsTarball, buildToolsTarballAlwaysFails)

	ctx, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(),
		"devcontroller", "dummy-cloud/region-1",
		"--config", "default-series=raring",
		"--config", "agent-stream=proposed",
		"--auto-upgrade",
	)

	c.Check(coretesting.Stderr(ctx), gc.Equals, fmt.Sprintf(`
Creating Juju controller "devcontroller" on dummy-cloud/region-1
Bootstrapping model %q
Starting new instance for initial controller
Building tools to upload (1.7.3.1-raring-%s)
`[1:], bootstrap.ControllerModelName, arch.HostArch()))
	c.Check(err, gc.ErrorMatches, "failed to bootstrap model: cannot upload bootstrap tools: an error")
}

func (s *BootstrapSuite) TestBootstrapDestroy(c *gc.C) {
	resetJujuXDGDataHome(c)
	s.patchVersion(c)

	opc, errc := cmdtesting.RunCommand(
		cmdtesting.NullContext(c), s.newBootstrapCommand(),
		"devcontroller", "dummy-cloud/region-1",
		"--config", "broken=Bootstrap Destroy",
		"--auto-upgrade",
	)
	err := <-errc
	c.Assert(err, gc.ErrorMatches, "failed to bootstrap model: dummy.Bootstrap is broken")
	var opDestroy *dummy.OpDestroy
	for opDestroy == nil {
		select {
		case op := <-opc:
			switch op := op.(type) {
			case dummy.OpDestroy:
				opDestroy = &op
			}
		default:
			c.Error("expected call to env.Destroy")
			return
		}
	}
	c.Assert(opDestroy.Error, gc.ErrorMatches, "dummy.Destroy is broken")
}

func (s *BootstrapSuite) TestBootstrapKeepBroken(c *gc.C) {
	resetJujuXDGDataHome(c)
	s.patchVersion(c)

	opc, errc := cmdtesting.RunCommand(cmdtesting.NullContext(c), s.newBootstrapCommand(),
		"--keep-broken",
		"devcontroller", "dummy-cloud/region-1",
		"--config", "broken=Bootstrap Destroy",
		"--auto-upgrade",
	)
	err := <-errc
	c.Assert(err, gc.ErrorMatches, "failed to bootstrap model: dummy.Bootstrap is broken")
	done := false
	for !done {
		select {
		case op, ok := <-opc:
			if !ok {
				done = true
				break
			}
			switch op.(type) {
			case dummy.OpDestroy:
				c.Error("unexpected call to env.Destroy")
				break
			}
		default:
			break
		}
	}
}

func (s *BootstrapSuite) TestBootstrapUnknownCloudOrProvider(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "no-such-provider")
	c.Assert(err, gc.ErrorMatches, `unknown cloud "no-such-provider", please try "juju update-clouds"`)
}

func (s *BootstrapSuite) TestBootstrapProviderNoRegionDetection(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "no-cloud-region-detection")
	c.Assert(err, gc.ErrorMatches, `unknown cloud "no-cloud-region-detection", please try "juju update-clouds"`)
}

func (s *BootstrapSuite) TestBootstrapProviderNoRegions(c *gc.C) {
	ctx, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "no-cloud-regions",
		"--config", "default-series=precise",
	)
	c.Check(coretesting.Stderr(ctx), gc.Matches, "Creating Juju controller \"ctrl\" on no-cloud-regions(.|\n)*")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *BootstrapSuite) TestBootstrapCloudNoRegions(c *gc.C) {
	resetJujuXDGDataHome(c)
	ctx, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy-cloud-without-regions",
		"--config", "default-series=precise",
	)
	c.Check(coretesting.Stderr(ctx), gc.Matches, "Creating Juju controller \"ctrl\" on dummy-cloud-without-regions(.|\n)*")
	c.Assert(err, jc.ErrorIsNil)
}

func (s *BootstrapSuite) TestBootstrapCloudNoRegionsOneSpecified(c *gc.C) {
	resetJujuXDGDataHome(c)
	ctx, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy-cloud-without-regions/my-region",
		"--config", "default-series=precise",
	)
	c.Check(coretesting.Stderr(ctx), gc.Matches,
		"region \"my-region\" not found \\(expected one of \\[\\]\\)\n\n.*")
	c.Assert(err, gc.Equals, cmd.ErrSilent)
}

func (s *BootstrapSuite) TestBootstrapProviderNoCredentials(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "no-credentials")
	c.Assert(err, gc.ErrorMatches, `detecting credentials for "no-credentials" cloud provider: credentials not found`)
}

func (s *BootstrapSuite) TestBootstrapProviderManyCredentials(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "many-credentials")
	c.Assert(err, gc.ErrorMatches, ambiguousCredentialError.Error())
}

func (s *BootstrapSuite) TestBootstrapProviderDetectRegionsInvalid(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	ctx, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "dummy/not-dummy")
	c.Assert(err, gc.Equals, cmd.ErrSilent)
	stderr := strings.Replace(coretesting.Stderr(ctx), "\n", "", -1)
	c.Assert(stderr, gc.Matches, `region "not-dummy" not found \(expected one of \["dummy"\]\)Specify an alternative region, or try "juju update-clouds".`)
}

func (s *BootstrapSuite) TestBootstrapProviderManyCredentialsCloudNoAuthTypes(c *gc.C) {
	var bootstrap fakeBootstrapFuncs
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	s.patchVersionAndSeries(c, "raring")
	s.store.Credentials = map[string]cloud.CloudCredential{
		"many-credentials-no-auth-types": {
			AuthCredentials: map[string]cloud.Credential{"one": cloud.NewCredential("one", nil)},
		},
	}
	coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl",
		"many-credentials-no-auth-types",
		"--credential", "one",
	)
	c.Assert(bootstrap.args.Cloud.AuthTypes, jc.SameContents, []cloud.AuthType{"one", "two"})
}

func (s *BootstrapSuite) TestBootstrapProviderDetectRegions(c *gc.C) {
	resetJujuXDGDataHome(c)

	var bootstrap fakeBootstrapFuncs
	bootstrap.cloudRegionDetector = cloudRegionDetectorFunc(func() ([]cloud.Region, error) {
		return []cloud.Region{{Name: "bruce", Endpoint: "endpoint"}}, nil
	})
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	s.patchVersionAndSeries(c, "raring")
	coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "dummy")
	c.Assert(bootstrap.args.CloudRegion, gc.Equals, "bruce")
	c.Assert(bootstrap.args.CloudCredentialName, gc.Equals, "default")
	c.Assert(bootstrap.args.Cloud, jc.DeepEquals, cloud.Cloud{
		Type:      "dummy",
		AuthTypes: []cloud.AuthType{cloud.EmptyAuthType},
		Regions:   []cloud.Region{{Name: "bruce", Endpoint: "endpoint"}},
	})
}

func (s *BootstrapSuite) TestBootstrapProviderDetectNoRegions(c *gc.C) {
	resetJujuXDGDataHome(c)

	var bootstrap fakeBootstrapFuncs
	bootstrap.cloudRegionDetector = cloudRegionDetectorFunc(func() ([]cloud.Region, error) {
		return nil, errors.NotFoundf("regions")
	})
	s.PatchValue(&getBootstrapFuncs, func() BootstrapInterface {
		return &bootstrap
	})

	s.patchVersionAndSeries(c, "raring")
	coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "dummy")
	c.Assert(bootstrap.args.CloudRegion, gc.Equals, "")
	c.Assert(bootstrap.args.Cloud, jc.DeepEquals, cloud.Cloud{
		Type:      "dummy",
		AuthTypes: []cloud.AuthType{cloud.EmptyAuthType},
	})
}

func (s *BootstrapSuite) TestBootstrapProviderCaseInsensitiveRegionCheck(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")

	var prepareParams bootstrap.PrepareParams
	s.PatchValue(&bootstrapPrepare, func(
		ctx environs.BootstrapContext,
		stor jujuclient.ClientStore,
		params bootstrap.PrepareParams,
	) (environs.Environ, error) {
		prepareParams = params
		return nil, fmt.Errorf("mock-prepare")
	})

	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "ctrl", "dummy/DUMMY")
	c.Assert(err, gc.ErrorMatches, "mock-prepare")
	c.Assert(prepareParams.CloudRegion, gc.Equals, "dummy")
}

func (s *BootstrapSuite) TestBootstrapConfigFile(c *gc.C) {
	tmpdir := c.MkDir()
	configFile := filepath.Join(tmpdir, "config.yaml")
	err := ioutil.WriteFile(configFile, []byte("controller: not-a-bool\n"), 0644)
	c.Assert(err, jc.ErrorIsNil)

	s.patchVersionAndSeries(c, "raring")
	_, err = coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy",
		"--config", configFile,
	)
	c.Assert(err, gc.ErrorMatches, `controller: expected bool, got string.*`)
}

func (s *BootstrapSuite) TestBootstrapMultipleConfigFiles(c *gc.C) {
	tmpdir := c.MkDir()
	configFile1 := filepath.Join(tmpdir, "config-1.yaml")
	err := ioutil.WriteFile(configFile1, []byte(
		"controller: not-a-bool\nbroken: Bootstrap\n",
	), 0644)
	c.Assert(err, jc.ErrorIsNil)
	configFile2 := filepath.Join(tmpdir, "config-2.yaml")
	err = ioutil.WriteFile(configFile2, []byte(
		"controller: false\n",
	), 0644)

	s.patchVersionAndSeries(c, "raring")
	_, err = coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy",
		"--auto-upgrade",
		// the second config file should replace attributes
		// with the same name from the first, but leave the
		// others alone.
		"--config", configFile1,
		"--config", configFile2,
	)
	c.Assert(err, gc.ErrorMatches, "failed to bootstrap model: dummy.Bootstrap is broken")
}

func (s *BootstrapSuite) TestBootstrapConfigFileAndAdHoc(c *gc.C) {
	tmpdir := c.MkDir()
	configFile := filepath.Join(tmpdir, "config.yaml")
	err := ioutil.WriteFile(configFile, []byte("controller: not-a-bool\n"), 0644)
	c.Assert(err, jc.ErrorIsNil)

	s.patchVersionAndSeries(c, "raring")
	_, err = coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy",
		"--auto-upgrade",
		// Configuration specified on the command line overrides
		// anything specified in files, no matter what the order.
		"--config", "controller=false",
		"--config", configFile,
	)
	c.Assert(err, jc.ErrorIsNil)
}

func (s *BootstrapSuite) TestBootstrapCloudConfigAndAdHoc(c *gc.C) {
	s.patchVersionAndSeries(c, "raring")
	_, err := coretesting.RunCommand(
		c, s.newBootstrapCommand(), "ctrl", "dummy-cloud-with-config",
		"--auto-upgrade",
		// Configuration specified on the command line overrides
		// anything specified in files, no matter what the order.
		"--config", "controller=false",
	)
	c.Assert(err, gc.ErrorMatches, "failed to bootstrap model: dummy.Bootstrap is broken")
}

func (s *BootstrapSuite) TestBootstrapPrintClouds(c *gc.C) {
	resetJujuXDGDataHome(c)
	s.store.Credentials = map[string]cloud.CloudCredential{
		"aws": {
			DefaultRegion: "us-west-1",
			AuthCredentials: map[string]cloud.Credential{
				"fred": {},
				"mary": {},
			},
		},
		"dummy-cloud": {
			DefaultRegion: "home",
			AuthCredentials: map[string]cloud.Credential{
				"joe": {},
			},
		},
	}
	defer func() {
		s.store = jujuclienttesting.NewMemStore()
	}()

	ctx, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "--clouds")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(coretesting.Stdout(ctx), jc.DeepEquals, `
You can bootstrap on these clouds. See ‘--regions <cloud>’ for all regions.
Cloud                           Credentials  Default Region
aws                             fred         us-west-1
                                mary         
aws-china                                    
aws-gov                                      
azure                                        
azure-china                                  
cloudsigma                                   
google                                       
joyent                                       
rackspace                                    
localhost                                    
dummy-cloud                     joe          home
dummy-cloud-with-config                      
dummy-cloud-without-regions                  
many-credentials-no-auth-types               

You will need to have a credential if you want to bootstrap on a cloud, see
‘juju autoload-credentials’ and ‘juju add-credential’. The first credential
listed is the default. Add more clouds with ‘juju add-cloud’.
`[1:])
}

func (s *BootstrapSuite) TestBootstrapPrintCloudRegions(c *gc.C) {
	resetJujuXDGDataHome(c)
	ctx, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "--regions", "aws")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(coretesting.Stdout(ctx), jc.DeepEquals, `
Showing regions for aws:
us-east-1
us-west-1
us-west-2
eu-west-1
eu-central-1
ap-southeast-1
ap-southeast-2
ap-northeast-1
ap-northeast-2
sa-east-1
`[1:])
}

func (s *BootstrapSuite) TestBootstrapPrintCloudRegionsNoSuchCloud(c *gc.C) {
	resetJujuXDGDataHome(c)
	_, err := coretesting.RunCommand(c, s.newBootstrapCommand(), "--regions", "foo")
	c.Assert(err, gc.ErrorMatches, "cloud foo not found")
}

// createToolsSource writes the mock tools and metadata into a temporary
// directory and returns it.
func createToolsSource(c *gc.C, versions []version.Binary) string {
	versionStrings := make([]string, len(versions))
	for i, vers := range versions {
		versionStrings[i] = vers.String()
	}
	source := c.MkDir()
	toolstesting.MakeTools(c, source, "released", versionStrings)
	return source
}

// resetJujuXDGDataHome restores an new, clean Juju home environment without tools.
func resetJujuXDGDataHome(c *gc.C) {
	jenvDir := testing.JujuXDGDataHomePath("models")
	err := os.RemoveAll(jenvDir)
	c.Assert(err, jc.ErrorIsNil)

	cloudsPath := cloud.JujuPersonalCloudsPath()
	err = ioutil.WriteFile(cloudsPath, []byte(`
clouds:
    dummy-cloud:
        type: dummy
        regions:
            region-1:
            region-2:
    dummy-cloud-without-regions:
        type: dummy
    dummy-cloud-with-config:
        type: dummy
        config:
            broken: Bootstrap
            controller: not-a-bool
    many-credentials-no-auth-types:
        type: many-credentials
`[1:]), 0644)
	c.Assert(err, jc.ErrorIsNil)
}

// checkTools check if the environment contains the passed envtools.
func checkTools(c *gc.C, env environs.Environ, expected []version.Binary) {
	list, err := envtools.FindTools(
		env, jujuversion.Current.Major, jujuversion.Current.Minor, "released", coretools.Filter{})
	c.Check(err, jc.ErrorIsNil)
	c.Logf("found: " + list.String())
	urls := list.URLs()
	c.Check(urls, gc.HasLen, len(expected))
}

var (
	v100d64 = version.MustParseBinary("1.0.0-raring-amd64")
	v100p64 = version.MustParseBinary("1.0.0-precise-amd64")
	v100q32 = version.MustParseBinary("1.0.0-quantal-i386")
	v100q64 = version.MustParseBinary("1.0.0-quantal-amd64")
	v120d64 = version.MustParseBinary("1.2.0-raring-amd64")
	v120p64 = version.MustParseBinary("1.2.0-precise-amd64")
	v120q32 = version.MustParseBinary("1.2.0-quantal-i386")
	v120q64 = version.MustParseBinary("1.2.0-quantal-amd64")
	v120t32 = version.MustParseBinary("1.2.0-trusty-i386")
	v120t64 = version.MustParseBinary("1.2.0-trusty-amd64")
	v190p32 = version.MustParseBinary("1.9.0-precise-i386")
	v190q64 = version.MustParseBinary("1.9.0-quantal-amd64")
	v200p64 = version.MustParseBinary("2.0.0-precise-amd64")
	v100All = []version.Binary{
		v100d64, v100p64, v100q64, v100q32,
	}
	v120All = []version.Binary{
		v120d64, v120p64, v120q64, v120q32, v120t32, v120t64,
	}
	v190All = []version.Binary{
		v190p32, v190q64,
	}
	v200All = []version.Binary{
		v200p64,
	}
	vAll = joinBinaryVersions(v100All, v120All, v190All, v200All)
)

func joinBinaryVersions(versions ...[]version.Binary) []version.Binary {
	var all []version.Binary
	for _, versions := range versions {
		all = append(all, versions...)
	}
	return all
}

// TODO(menn0): This fake BootstrapInterface implementation is
// currently quite minimal but could be easily extended to cover more
// test scenarios. This could help improve some of the tests in this
// file which execute large amounts of external functionality.
type fakeBootstrapFuncs struct {
	args                bootstrap.BootstrapParams
	cloudRegionDetector environs.CloudRegionDetector
}

func (fake *fakeBootstrapFuncs) Bootstrap(ctx environs.BootstrapContext, env environs.Environ, args bootstrap.BootstrapParams) error {
	fake.args = args
	return nil
}

func (fake *fakeBootstrapFuncs) CloudRegionDetector(environs.EnvironProvider) (environs.CloudRegionDetector, bool) {
	detector := fake.cloudRegionDetector
	if detector == nil {
		detector = cloudRegionDetectorFunc(func() ([]cloud.Region, error) {
			return nil, errors.NotFoundf("regions")
		})
	}
	return detector, true
}

type noCloudRegionDetectionProvider struct {
	environs.EnvironProvider
}

type noCloudRegionsProvider struct {
	environs.EnvironProvider
}

func (noCloudRegionsProvider) DetectRegions() ([]cloud.Region, error) {
	return nil, errors.NotFoundf("regions")
}

func (noCloudRegionsProvider) CredentialSchemas() map[cloud.AuthType]cloud.CredentialSchema {
	return nil
}

type noCredentialsProvider struct {
	environs.EnvironProvider
}

func (noCredentialsProvider) DetectRegions() ([]cloud.Region, error) {
	return []cloud.Region{{Name: "region"}}, nil
}

func (noCredentialsProvider) DetectCredentials() (*cloud.CloudCredential, error) {
	return nil, errors.NotFoundf("credentials")
}

func (noCredentialsProvider) CredentialSchemas() map[cloud.AuthType]cloud.CredentialSchema {
	return nil
}

type manyCredentialsProvider struct {
	environs.EnvironProvider
}

func (manyCredentialsProvider) DetectRegions() ([]cloud.Region, error) {
	return []cloud.Region{{Name: "region"}}, nil
}

func (manyCredentialsProvider) DetectCredentials() (*cloud.CloudCredential, error) {
	return &cloud.CloudCredential{
		AuthCredentials: map[string]cloud.Credential{
			"one": cloud.NewCredential("one", nil),
			"two": {},
		},
	}, nil
}

func (manyCredentialsProvider) CredentialSchemas() map[cloud.AuthType]cloud.CredentialSchema {
	return map[cloud.AuthType]cloud.CredentialSchema{"one": {}, "two": {}}
}

type cloudRegionDetectorFunc func() ([]cloud.Region, error)

func (c cloudRegionDetectorFunc) DetectRegions() ([]cloud.Region, error) {
	return c()
}
