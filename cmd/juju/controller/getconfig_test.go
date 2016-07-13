// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package controller_test

import (
	"strings"

	"github.com/juju/cmd"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/errors"
	"github.com/juju/juju/cmd/juju/controller"
	jujucontroller "github.com/juju/juju/controller"
	"github.com/juju/juju/testing"
)

type GetConfigSuite struct {
	baseControllerSuite
}

var _ = gc.Suite(&GetConfigSuite{})

func (s *GetConfigSuite) SetUpTest(c *gc.C) {
	s.baseControllerSuite.SetUpTest(c)
	s.createTestClientStore(c)
}

func (s *GetConfigSuite) run(c *gc.C, args ...string) (*cmd.Context, error) {
	command := controller.NewGetConfigCommandForTest(&fakeControllerAPI{}, s.store)
	return testing.RunCommand(c, command, args...)
}

func (s *GetConfigSuite) TestInit(c *gc.C) {
	// zero or one args is fine.
	err := testing.InitCommand(controller.NewGetConfigCommandForTest(&fakeControllerAPI{}, s.store), nil)
	c.Check(err, jc.ErrorIsNil)
	err = testing.InitCommand(controller.NewGetConfigCommandForTest(&fakeControllerAPI{}, s.store), []string{"one"})
	c.Check(err, jc.ErrorIsNil)
	// More than one is not allowed.
	err = testing.InitCommand(controller.NewGetConfigCommandForTest(&fakeControllerAPI{}, s.store), []string{"one", "two"})
	c.Check(err, gc.ErrorMatches, `unrecognized args: \["two"\]`)
}

func (s *GetConfigSuite) TestSingleValue(c *gc.C) {
	context, err := s.run(c, "controller-uuid")
	c.Assert(err, jc.ErrorIsNil)

	output := strings.TrimSpace(testing.Stdout(context))
	c.Assert(output, gc.Equals, "uuid")
}

func (s *GetConfigSuite) TestSingleValueJSON(c *gc.C) {
	context, err := s.run(c, "--format=json", "controller-uuid")
	c.Assert(err, jc.ErrorIsNil)

	output := strings.TrimSpace(testing.Stdout(context))
	c.Assert(output, gc.Equals, `"uuid"`)
}

func (s *GetConfigSuite) TestAllValues(c *gc.C) {
	context, err := s.run(c)
	c.Assert(err, jc.ErrorIsNil)

	output := strings.TrimSpace(testing.Stdout(context))
	expected := "" +
		"api-port: 1234\n" +
		"controller-uuid: uuid"
	c.Assert(output, gc.Equals, expected)
}

func (s *GetConfigSuite) TestAllValuesJSON(c *gc.C) {
	context, err := s.run(c, "--format=json")
	c.Assert(err, jc.ErrorIsNil)

	output := strings.TrimSpace(testing.Stdout(context))
	expected := `{"api-port":1234,"controller-uuid":"uuid"}`
	c.Assert(output, gc.Equals, expected)
}

func (s *GetConfigSuite) TestError(c *gc.C) {
	command := controller.NewGetConfigCommandForTest(&fakeControllerAPI{err: errors.New("error")}, s.store)
	_, err := testing.RunCommand(c, command)
	c.Assert(err, gc.ErrorMatches, "error")
}

type fakeControllerAPI struct {
	err error
}

func (f *fakeControllerAPI) Close() error {
	return nil
}

func (f *fakeControllerAPI) ControllerConfig() (jujucontroller.Config, error) {
	if f.err != nil {
		return nil, f.err
	}
	return map[string]interface{}{
		"controller-uuid": "uuid",
		"api-port":        1234,
	}, nil
}
