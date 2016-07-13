// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package maas

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/juju/errors"
	"github.com/juju/utils"

	"github.com/juju/juju/cloud"
)

type environProviderCredentials struct{}

// CredentialSchemas is part of the environs.ProviderCredentials interface.
func (environProviderCredentials) CredentialSchemas() map[cloud.AuthType]cloud.CredentialSchema {
	return map[cloud.AuthType]cloud.CredentialSchema{
		cloud.OAuth1AuthType: {
			{
				"maas-oauth", cloud.CredentialAttr{
					Description: "OAuth/API-key credentials for MAAS",
					Hidden:      true,
				},
			},
		},
	}
}

// DetectCredentials is part of the environs.ProviderCredentials interface.
func (environProviderCredentials) DetectCredentials() (*cloud.CloudCredential, error) {
	// MAAS stores credentials in a json file: ~/.maasrc
	// {"Server": "http://<ip>/MAAS", "OAuth": "<key>"}
	maasrc := filepath.Join(utils.Home(), ".maasrc")
	fileBytes, err := ioutil.ReadFile(maasrc)
	if err != nil {
		return nil, errors.Trace(err)
	}

	details := make(map[string]interface{})
	err = json.Unmarshal(fileBytes, &details)
	if err != nil {
		return nil, errors.Trace(err)
	}
	oauthKey := details["OAuth"]
	if oauthKey == "" {
		return nil, errors.New("MAAS credentials require a value for OAuth token")
	}
	cred := cloud.NewCredential(cloud.OAuth1AuthType, map[string]string{
		"maas-oauth": fmt.Sprintf("%v", oauthKey),
	})
	server, ok := details["Server"]
	if server == "" || !ok {
		server = "unspecified server"
	}
	cred.Label = fmt.Sprintf("MAAS credential for %s", server)

	return &cloud.CloudCredential{
		AuthCredentials: map[string]cloud.Credential{
			"default": cred,
		}}, nil
}
