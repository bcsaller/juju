// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package logfwd

import (
	"io"

	"github.com/juju/errors"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/apiserver/common"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/state"
)

func init() {
	common.RegisterStandardFacade("LogForwarding", 1, func(st *state.State, _ *common.Resources, auth common.Authorizer) (*LogForwardingAPI, error) {
		return NewLogForwardingAPI(&stateAdapter{st}, auth)
	})
}

// LastSentTracker exposes the functionality of state.LastSentTracker.
type LastSentTracker interface {
	io.Closer

	// Get retrieves the record ID.
	Get() (int64, error)

	// Set records the record ID.
	Set(recID int64) error
}

// LogForwardingState supports interacting with state for the
// LogForwarding facade.
type LogForwardingState interface {
	// NewLastSentTracker creates a new tracker for the given model
	// and log sink.
	NewLastSentTracker(tag names.ModelTag, sink string) (LastSentTracker, error)
}

// LogForwardingAPI is the concrete implementation of the api end point.
type LogForwardingAPI struct {
	state LogForwardingState
}

// NewLogForwardingAPI creates a new server-side logger API end point.
func NewLogForwardingAPI(st LogForwardingState, auth common.Authorizer) (*LogForwardingAPI, error) {
	if !auth.AuthMachineAgent() { // the controller's machine agent
		return nil, common.ErrPerm
	}
	api := &LogForwardingAPI{
		state: st,
	}
	return api, nil
}

// GetLastSent is a bulk call that gets the log forwarding "last sent"
// record ID for each requested target.
func (api *LogForwardingAPI) GetLastSent(args params.LogForwardingGetLastSentParams) params.LogForwardingGetLastSentResults {
	results := make([]params.LogForwardingGetLastSentResult, len(args.IDs))
	for i, id := range args.IDs {
		results[i] = api.get(id)
	}
	return params.LogForwardingGetLastSentResults{
		Results: results,
	}
}

func (api *LogForwardingAPI) get(id params.LogForwardingID) params.LogForwardingGetLastSentResult {
	var res params.LogForwardingGetLastSentResult
	lst, err := api.newLastSentTracker(id)
	if err != nil {
		res.Error = common.ServerError(err)
		return res
	}
	defer lst.Close()

	recID, err := lst.Get()
	if err != nil {
		res.Error = common.ServerError(err)
		if errors.Cause(err) == state.ErrNeverForwarded {
			res.Error.Code = params.CodeNotFound
		}
		return res
	}
	res.RecordID = recID
	return res
}

// SetLastSent is a bulk call that sets the log forwarding "last sent"
// record ID for each requested target.
func (api *LogForwardingAPI) SetLastSent(args params.LogForwardingSetLastSentParams) params.ErrorResults {
	results := make([]params.ErrorResult, len(args.Params), len(args.Params))
	for i, arg := range args.Params {
		results[i].Error = api.set(arg)
	}
	return params.ErrorResults{
		Results: results,
	}
}

func (api *LogForwardingAPI) set(arg params.LogForwardingSetLastSentParam) *params.Error {
	lst, err := api.newLastSentTracker(arg.LogForwardingID)
	if err != nil {
		return common.ServerError(err)
	}
	defer lst.Close()

	err = lst.Set(arg.RecordID)
	return common.ServerError(err)
}

func (api *LogForwardingAPI) newLastSentTracker(id params.LogForwardingID) (LastSentTracker, error) {
	tag, err := names.ParseModelTag(id.ModelTag)
	if err != nil {
		return nil, err
	}
	tracker, err := api.state.NewLastSentTracker(tag, id.Sink)
	if err != nil {
		return nil, err
	}
	return tracker, nil
}

type stateAdapter struct {
	*state.State
}

// NewLastSentTracker implements LogForwardingState.
func (st stateAdapter) NewLastSentTracker(tag names.ModelTag, sink string) (LastSentTracker, error) {
	if _, err := st.GetModel(tag); err != nil {
		return nil, err
	}
	loggingState, err := st.ForModel(tag)
	if err != nil {
		return nil, err
	}
	lastSent := state.NewLastSentLogTracker(loggingState, sink)
	return &lastSentCloser{lastSent, loggingState}, nil
}

type lastSentCloser struct {
	*state.LastSentLogTracker
	io.Closer
}
