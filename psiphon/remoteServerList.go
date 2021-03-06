/*
 * Copyright (c) 2015, Psiphon Inc.
 * All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package psiphon

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
)

// RemoteServerList is a JSON record containing a list of Psiphon server
// entries. As it may be downloaded from various sources, it is digitally
// signed so that the data may be authenticated.
type RemoteServerList struct {
	Data                   string `json:"data"`
	SigningPublicKeyDigest string `json:"signingPublicKeyDigest"`
	Signature              string `json:"signature"`
}

// FetchRemoteServerList downloads a remote server list JSON record from
// config.RemoteServerListUrl; validates its digital signature using the
// public key config.RemoteServerListSignaturePublicKey; and parses the
// data field into ServerEntry records.
func FetchRemoteServerList(config *Config, pendingConns *Conns) (err error) {
	NoticeInfo("fetching remote server list")

	// Note: pendingConns may be used to interrupt the fetch remote server list
	// request. BindToDevice may be used to exclude requests from VPN routing.
	dialConfig := &DialConfig{
		UpstreamHttpProxyAddress: config.UpstreamHttpProxyAddress,
		PendingConns:             pendingConns,
		BindToDeviceProvider:     config.BindToDeviceProvider,
		BindToDeviceDnsServer:    config.BindToDeviceDnsServer,
	}
	transport := &http.Transport{
		Dial: NewTCPDialer(dialConfig),
	}
	httpClient := http.Client{
		Timeout:   FETCH_REMOTE_SERVER_LIST_TIMEOUT,
		Transport: transport,
	}

	response, err := httpClient.Get(config.RemoteServerListUrl)
	if err != nil {
		return ContextError(err)
	}
	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return ContextError(err)
	}

	var remoteServerList *RemoteServerList
	err = json.Unmarshal(body, &remoteServerList)
	if err != nil {
		return ContextError(err)
	}
	err = validateRemoteServerList(config, remoteServerList)
	if err != nil {
		return ContextError(err)
	}

	serverEntries, err := DecodeAndValidateServerEntryList(remoteServerList.Data)
	if err != nil {
		return ContextError(err)
	}

	err = StoreServerEntries(serverEntries, true)
	if err != nil {
		return ContextError(err)
	}

	return nil
}

func validateRemoteServerList(config *Config, remoteServerList *RemoteServerList) (err error) {
	derEncodedPublicKey, err := base64.StdEncoding.DecodeString(config.RemoteServerListSignaturePublicKey)
	if err != nil {
		return ContextError(err)
	}
	publicKey, err := x509.ParsePKIXPublicKey(derEncodedPublicKey)
	if err != nil {
		return ContextError(err)
	}
	rsaPublicKey, ok := publicKey.(*rsa.PublicKey)
	if !ok {
		return ContextError(errors.New("unexpected RemoteServerListSignaturePublicKey key type"))
	}
	signature, err := base64.StdEncoding.DecodeString(remoteServerList.Signature)
	if err != nil {
		return ContextError(err)
	}
	// TODO: can detect if signed with different key --
	// match digest(publicKey) against remoteServerList.signingPublicKeyDigest
	hash := sha256.New()
	hash.Write([]byte(remoteServerList.Data))
	digest := hash.Sum(nil)
	err = rsa.VerifyPKCS1v15(rsaPublicKey, crypto.SHA256, digest, signature)
	if err != nil {
		return ContextError(err)
	}
	return nil
}
