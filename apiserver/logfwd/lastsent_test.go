// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package logfwd_test

import (
	"github.com/juju/errors"
	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/logfwd"
	"github.com/juju/juju/apiserver/params"
	apiservertesting "github.com/juju/juju/apiserver/testing"
	"github.com/juju/juju/state"
)

type LastSentSuite struct {
	testing.IsolationSuite

	stub       *testing.Stub
	state      *stubState
	machineTag names.MachineTag
	authorizer apiservertesting.FakeAuthorizer
}

var _ = gc.Suite(&LastSentSuite{})

func (s *LastSentSuite) SetUpTest(c *gc.C) {
	s.IsolationSuite.SetUpTest(c)

	s.stub = &testing.Stub{}
	s.state = &stubState{stub: s.stub}
	s.machineTag = names.NewMachineTag("99")
	// The default auth is as the machine agent
	s.authorizer = apiservertesting.FakeAuthorizer{
		Tag: s.machineTag,
	}
}

func (s *LastSentSuite) TestAuthRefusesUser(c *gc.C) {
	anAuthorizer := s.authorizer
	anAuthorizer.Tag = names.NewUserTag("bob")

	_, err := logfwd.NewLogForwardingAPI(s.state, anAuthorizer)

	c.Check(err, gc.ErrorMatches, "permission denied")
}

func (s *LastSentSuite) TestGetLastSentOne(c *gc.C) {
	tracker := s.state.addTracker()
	tracker.ReturnGet = 10
	api, err := logfwd.NewLogForwardingAPI(s.state, s.authorizer)
	c.Assert(err, jc.ErrorIsNil)
	model := "deadbeef-2f18-4fd2-967d-db9663db7bea"
	modelTag := names.NewModelTag(model)

	res := api.GetLastSent(params.LogForwardingGetLastSentParams{
		IDs: []params.LogForwardingID{{
			ModelTag: modelTag.String(),
			Sink:     "spam",
		}},
	})

	c.Check(res, jc.DeepEquals, params.LogForwardingGetLastSentResults{
		Results: []params.LogForwardingGetLastSentResult{{
			RecordID: 10,
		}},
	})
	s.stub.CheckCallNames(c, "NewLastSentTracker", "Get", "Close")
	s.stub.CheckCall(c, 0, "NewLastSentTracker", modelTag, "spam")
}

func (s *LastSentSuite) TestGetLastSentBulk(c *gc.C) {
	trackerSpam := s.state.addTracker()
	trackerSpam.ReturnGet = 10
	trackerEggs := s.state.addTracker()
	trackerEggs.ReturnGet = 20
	s.state.addTracker() // ham
	s.stub.SetErrors(nil, nil, nil, nil, nil, nil, nil, state.ErrNeverForwarded)
	api, err := logfwd.NewLogForwardingAPI(s.state, s.authorizer)
	c.Assert(err, jc.ErrorIsNil)
	model := "deadbeef-2f18-4fd2-967d-db9663db7bea"
	modelTag := names.NewModelTag(model)

	res := api.GetLastSent(params.LogForwardingGetLastSentParams{
		IDs: []params.LogForwardingID{{
			ModelTag: modelTag.String(),
			Sink:     "spam",
		}, {
			ModelTag: modelTag.String(),
			Sink:     "eggs",
		}, {
			ModelTag: modelTag.String(),
			Sink:     "ham",
		}},
	})

	c.Check(res, jc.DeepEquals, params.LogForwardingGetLastSentResults{
		Results: []params.LogForwardingGetLastSentResult{{
			RecordID: 10,
		}, {
			RecordID: 20,
		}, {
			Error: &params.Error{
				Message: `cannot find ID of the last forwarded record`,
				Code:    params.CodeNotFound,
			},
		}},
	})
	s.stub.CheckCallNames(c,
		"NewLastSentTracker", "Get", "Close",
		"NewLastSentTracker", "Get", "Close",
		"NewLastSentTracker", "Get", "Close",
	)
	s.stub.CheckCall(c, 0, "NewLastSentTracker", modelTag, "spam")
	s.stub.CheckCall(c, 3, "NewLastSentTracker", modelTag, "eggs")
	s.stub.CheckCall(c, 6, "NewLastSentTracker", modelTag, "ham")
}

func (s *LastSentSuite) TestSetLastSentOne(c *gc.C) {
	s.state.addTracker()
	api, err := logfwd.NewLogForwardingAPI(s.state, s.authorizer)
	c.Assert(err, jc.ErrorIsNil)
	model := "deadbeef-2f18-4fd2-967d-db9663db7bea"
	modelTag := names.NewModelTag(model)

	res := api.SetLastSent(params.LogForwardingSetLastSentParams{
		Params: []params.LogForwardingSetLastSentParam{{
			LogForwardingID: params.LogForwardingID{
				ModelTag: modelTag.String(),
				Sink:     "spam",
			},
			RecordID: 10,
		}},
	})

	c.Check(res, jc.DeepEquals, params.ErrorResults{
		Results: []params.ErrorResult{{
			Error: nil,
		}},
	})
	s.stub.CheckCallNames(c, "NewLastSentTracker", "Set", "Close")
	s.stub.CheckCall(c, 0, "NewLastSentTracker", modelTag, "spam")
	s.stub.CheckCall(c, 1, "Set", int64(10))
}

func (s *LastSentSuite) TestSetLastSentBulk(c *gc.C) {
	s.state.addTracker() // spam
	s.state.addTracker() // eggs
	s.state.addTracker() // ham
	failure := errors.New("<failed>")
	s.stub.SetErrors(nil, nil, nil, nil, failure)
	api, err := logfwd.NewLogForwardingAPI(s.state, s.authorizer)
	c.Assert(err, jc.ErrorIsNil)
	model := "deadbeef-2f18-4fd2-967d-db9663db7bea"
	modelTag := names.NewModelTag(model)

	res := api.SetLastSent(params.LogForwardingSetLastSentParams{
		Params: []params.LogForwardingSetLastSentParam{{
			LogForwardingID: params.LogForwardingID{
				ModelTag: modelTag.String(),
				Sink:     "spam",
			},
			RecordID: 10,
		}, {
			LogForwardingID: params.LogForwardingID{
				ModelTag: modelTag.String(),
				Sink:     "eggs",
			},
			RecordID: 20,
		}, {
			LogForwardingID: params.LogForwardingID{
				ModelTag: modelTag.String(),
				Sink:     "ham",
			},
			RecordID: 15,
		}},
	})

	c.Check(res, jc.DeepEquals, params.ErrorResults{
		Results: []params.ErrorResult{{
			Error: nil,
		}, {
			Error: common.ServerError(failure),
		}, {
			Error: nil,
		}},
	})
	s.stub.CheckCallNames(c,
		"NewLastSentTracker", "Set", "Close",
		"NewLastSentTracker", "Set", "Close",
		"NewLastSentTracker", "Set", "Close",
	)
	s.stub.CheckCall(c, 0, "NewLastSentTracker", modelTag, "spam")
	s.stub.CheckCall(c, 1, "Set", int64(10))
	s.stub.CheckCall(c, 3, "NewLastSentTracker", modelTag, "eggs")
	s.stub.CheckCall(c, 4, "Set", int64(20))
	s.stub.CheckCall(c, 6, "NewLastSentTracker", modelTag, "ham")
	s.stub.CheckCall(c, 7, "Set", int64(15))
}

type stubState struct {
	stub *testing.Stub

	ReturnNewLastSentTracker []logfwd.LastSentTracker
}

func (s *stubState) addTracker() *stubTracker {
	tracker := &stubTracker{stub: s.stub}
	s.ReturnNewLastSentTracker = append(s.ReturnNewLastSentTracker, tracker)
	return tracker
}

func (s *stubState) NewLastSentTracker(tag names.ModelTag, sink string) (logfwd.LastSentTracker, error) {
	s.stub.AddCall("NewLastSentTracker", tag, sink)
	if err := s.stub.NextErr(); err != nil {
		return nil, err
	}

	if len(s.ReturnNewLastSentTracker) == 0 {
		panic("ran out of trackers")
	}
	tracker := s.ReturnNewLastSentTracker[0]
	s.ReturnNewLastSentTracker = s.ReturnNewLastSentTracker[1:]
	return tracker, nil
}

type stubTracker struct {
	stub *testing.Stub

	ReturnGet int64
}

func (s *stubTracker) Get() (int64, error) {
	s.stub.AddCall("Get")
	if err := s.stub.NextErr(); err != nil {
		return 0, err
	}

	return s.ReturnGet, nil
}

func (s *stubTracker) Set(recID int64) error {
	s.stub.AddCall("Set", recID)
	if err := s.stub.NextErr(); err != nil {
		return err
	}

	return nil
}

func (s *stubTracker) Close() error {
	s.stub.AddCall("Close")
	if err := s.stub.NextErr(); err != nil {
		return err
	}

	return nil
}
