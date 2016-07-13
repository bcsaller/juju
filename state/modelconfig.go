// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"github.com/juju/errors"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"gopkg.in/mgo.v2/txn"

	"github.com/juju/juju/controller"
	"github.com/juju/juju/environs/config"
)

var disallowedModelConfigAttrs = [...]string{
	"admin-secret",
	"ca-private-key",
}

// ModelConfig returns the complete config for the model represented
// by this state.
func (st *State) ModelConfig() (*config.Config, error) {
	modelSettings, err := readSettings(st, settingsC, modelGlobalKey)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return config.New(config.NoDefaults, modelSettings.Map())
}

// checkModelConfig returns an error if the config is definitely invalid.
func checkModelConfig(cfg *config.Config) error {
	allAttrs := cfg.AllAttrs()
	for _, attr := range disallowedModelConfigAttrs {
		if _, ok := allAttrs[attr]; ok {
			return errors.Errorf(attr + " should never be written to the state")
		}
	}
	if _, ok := cfg.AgentVersion(); !ok {
		return errors.Errorf("agent-version must always be set in state")
	}
	for attr := range allAttrs {
		if controller.ControllerOnlyAttribute(attr) {
			return errors.Errorf("cannot set controller attribute %q on a model", attr)
		}
	}
	return nil
}

// ModelConfigValues returns the config values for the model represented
// by this state.
func (st *State) ModelConfigValues() (config.ConfigValues, error) {
	modelSettings, err := readSettings(st, settingsC, modelGlobalKey)
	if err != nil {
		return nil, errors.Trace(err)
	}
	// TODO(wallyworld) - this data should not be stored separately
	sources, closer := st.getCollection(modelSettingsSourcesC)
	defer closer()

	var settingsSources settingsSourcesDoc
	err = sources.FindId(modelGlobalKey).One(&settingsSources)
	if err == mgo.ErrNotFound {
		err = errors.NotFoundf("settings sources")
	}
	if err != nil {
		return nil, err
	}
	result := make(config.ConfigValues)
	for attr, val := range modelSettings.Map() {
		source, ok := settingsSources.Sources[attr]
		if !ok {
			source = config.JujuModelConfigSource
		}
		result[attr] = config.ConfigValue{
			Value:  val,
			Source: source,
		}
	}
	return result, nil
}

// checkControllerInheritedConfig returns an error if the shared local cloud config is definitely invalid.
func checkControllerInheritedConfig(attrs map[string]interface{}) error {
	disallowedCloudConfigAttrs := append(disallowedModelConfigAttrs[:], config.AgentVersionKey)
	for _, attr := range disallowedCloudConfigAttrs {
		if _, ok := attrs[attr]; ok {
			return errors.Errorf("local cloud config cannot contain " + attr)
		}
	}
	for attrName := range attrs {
		if controller.ControllerOnlyAttribute(attrName) {
			return errors.Errorf("local cloud config cannot contain controller attribute %q", attrName)
		}
	}
	return nil
}

func (st *State) buildAndValidateModelConfig(updateAttrs map[string]interface{}, removeAttrs []string, oldConfig *config.Config) (validCfg *config.Config, err error) {
	newConfig, err := oldConfig.Apply(updateAttrs)
	if err != nil {
		return nil, errors.Trace(err)
	}
	if len(removeAttrs) != 0 {
		newConfig, err = newConfig.Remove(removeAttrs)
		if err != nil {
			return nil, errors.Trace(err)
		}
	}
	if err := checkModelConfig(newConfig); err != nil {
		return nil, errors.Trace(err)
	}
	return st.validate(newConfig, oldConfig)
}

type ValidateConfigFunc func(updateAttrs map[string]interface{}, removeAttrs []string, oldConfig *config.Config) error

// UpdateModelConfig adds, updates or removes attributes in the current
// configuration of the model with the provided updateAttrs and
// removeAttrs.
func (st *State) UpdateModelConfig(updateAttrs map[string]interface{}, removeAttrs []string, additionalValidation ValidateConfigFunc) error {
	if len(updateAttrs)+len(removeAttrs) == 0 {
		return nil
	}

	// TODO(axw) 2013-12-6 #1167616
	// Ensure that the settings on disk have not changed
	// underneath us. The settings changes are actually
	// applied as a delta to what's on disk; if there has
	// been a concurrent update, the change may not be what
	// the user asked for.

	modelSettings, err := readSettings(st, settingsC, modelGlobalKey)
	if err != nil {
		return errors.Trace(err)
	}

	// Get the existing model config from state.
	oldConfig, err := st.ModelConfig()
	if err != nil {
		return errors.Trace(err)
	}
	if additionalValidation != nil {
		err = additionalValidation(updateAttrs, removeAttrs, oldConfig)
		if err != nil {
			return errors.Trace(err)
		}
	}
	validCfg, err := st.buildAndValidateModelConfig(updateAttrs, removeAttrs, oldConfig)
	if err != nil {
		return errors.Trace(err)
	}

	validAttrs := validCfg.AllAttrs()
	for k := range oldConfig.AllAttrs() {
		if _, ok := validAttrs[k]; !ok {
			modelSettings.Delete(k)
		}
	}

	modelSettings.Update(validAttrs)
	changes, ops := modelSettings.settingsUpdateOps()
	ops = append(ops, updateModelSourcesOps(changes)...)
	return modelSettings.write(ops)
}

func updateModelSourcesOps(changes []ItemChange) []txn.Op {
	var update bson.D
	var set = make(bson.M)
	for _, c := range changes {
		set["sources."+c.Key] = "model"
	}
	update = append(update, bson.DocElem{"$set", set})

	ops := []txn.Op{{
		C:      modelSettingsSourcesC,
		Id:     modelGlobalKey,
		Assert: txn.DocExists,
		Update: update,
	}}
	return ops
}

// settingsSourcesDoc stores for each model attribute,
// the source of the attribute.
type settingsSourcesDoc struct {
	// Sources defines the named source for each settings attribute.
	Sources map[string]string `bson:"sources,omitempty"`
}

func createSettingsSourceOp(values map[string]string) txn.Op {
	return txn.Op{
		C:      modelSettingsSourcesC,
		Id:     modelGlobalKey,
		Assert: txn.DocMissing,
		Insert: &settingsSourcesDoc{
			Sources: values,
		},
	}
}

type modelConfigSourceFunc func() (map[string]interface{}, error)

type modelConfigSource struct {
	name       string
	sourceFunc modelConfigSourceFunc
}

// modelConfigSources returns a slice of named model config
// sources, in hierarchical order. Starting from the first source,
// config is retrieved and each subsequent source adds to the
// overall config values, later values override earlier ones.
func modelConfigSources(st *State) []modelConfigSource {
	return []modelConfigSource{
		{config.JujuControllerSource, st.ControllerInheritedConfig},
		// We will also support local cloud region, tenant, user etc
	}
}

// ControllerInheritedConfig returns the inherited config values
// sourced from the local cloud config.
func (st *State) ControllerInheritedConfig() (map[string]interface{}, error) {
	settings, err := readSettings(st, globalSettingsC, controllerInheritedSettingsGlobalKey)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return settings.Map(), nil
}

// composeModelConfigAttributes returns a set of model config settings composed from known
// sources of default values overridden by model specific attributes.
// Also returned is a map containing the source location for each model attribute.
// The source location is the name of the config values from which an attribute came.
func composeModelConfigAttributes(
	modelAttr map[string]interface{}, configSources ...modelConfigSource,
) (map[string]interface{}, map[string]string, error) {
	resultAttrs := make(map[string]interface{})
	settingsSources := make(map[string]string)

	// Compose default settings from all known sources.
	for _, source := range configSources {
		newSettings, err := source.sourceFunc()
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, errors.Annotatef(err, "reading %s settings", source.name)
		}
		for name, val := range newSettings {
			resultAttrs[name] = val
			settingsSources[name] = source.name
		}
	}

	// Merge in model specific settings.
	for attr, val := range modelAttr {
		resultAttrs[attr] = val
		settingsSources[attr] = config.JujuModelConfigSource
	}

	return resultAttrs, settingsSources, nil
}
