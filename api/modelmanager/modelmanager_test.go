// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package modelmanager_test

import (
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/api/modelmanager"
	jujutesting "github.com/juju/juju/juju/testing"
	"github.com/juju/juju/testing/factory"
)

type modelmanagerSuite struct {
	jujutesting.JujuConnSuite
}

var _ = gc.Suite(&modelmanagerSuite{})

func (s *modelmanagerSuite) SetUpTest(c *gc.C) {
	s.JujuConnSuite.SetUpTest(c)
}

func (s *modelmanagerSuite) OpenAPI(c *gc.C) *modelmanager.Client {
	return modelmanager.NewClient(s.APIState)
}

func (s *modelmanagerSuite) TestCreateModelBadUser(c *gc.C) {
	modelManager := s.OpenAPI(c)
	_, err := modelManager.CreateModel("mymodel", "not a user", "", "", nil)
	c.Assert(err, gc.ErrorMatches, `invalid owner name "not a user"`)
}

func (s *modelmanagerSuite) TestCreateModel(c *gc.C) {
	modelManager := s.OpenAPI(c)
	user := s.Factory.MakeUser(c, nil)
	owner := user.UserTag().Canonical()
	newModel, err := modelManager.CreateModel("new-model", owner, "", "", map[string]interface{}{
		"authorized-keys": "ssh-key",
		// dummy needs controller
		"controller": false,
	})
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(newModel.Name, gc.Equals, "new-model")
	c.Assert(newModel.OwnerTag, gc.Equals, user.Tag().String())
	c.Assert(newModel.CloudRegion, gc.Equals, "")
	c.Assert(utils.IsValidUUIDString(newModel.UUID), jc.IsTrue)
}

func (s *modelmanagerSuite) TestListModelsBadUser(c *gc.C) {
	modelManager := s.OpenAPI(c)
	_, err := modelManager.ListModels("not a user")
	c.Assert(err, gc.ErrorMatches, `invalid user name "not a user"`)
}

func (s *modelmanagerSuite) TestListModels(c *gc.C) {
	owner := names.NewUserTag("user@remote")
	s.Factory.MakeModel(c, &factory.ModelParams{
		Name: "first", Owner: owner}).Close()
	s.Factory.MakeModel(c, &factory.ModelParams{
		Name: "second", Owner: owner}).Close()

	modelManager := s.OpenAPI(c)
	models, err := modelManager.ListModels("user@remote")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(models, gc.HasLen, 2)

	modelNames := []string{models[0].Name, models[1].Name}
	c.Assert(modelNames, jc.DeepEquals, []string{"first", "second"})
	ownerNames := []string{models[0].Owner, models[1].Owner}
	c.Assert(ownerNames, jc.DeepEquals, []string{"user@remote", "user@remote"})
}

func (s *modelmanagerSuite) TestDestroyEnvironment(c *gc.C) {
	modelManagerClient := s.OpenAPI(c)
	var called bool
	modelmanager.PatchFacadeCall(&s.CleanupSuite, modelManagerClient,
		func(req string, args interface{}, resp interface{}) error {
			c.Assert(req, gc.Equals, "DestroyModel")
			called = true
			return nil
		})

	err := modelManagerClient.DestroyModel()
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(called, jc.IsTrue)
}
