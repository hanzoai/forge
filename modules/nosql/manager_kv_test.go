// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nosql

import (
	"net/url"
	"testing"
)

func TestKVUsernameOpt(t *testing.T) {
	uri, _ := url.Parse("kv://user:password@kvhost/0")
	opts := getKVOptions(uri)

	if opts.Username != "user" {
		t.Fail()
	}
}

func TestKVPasswordOpt(t *testing.T) {
	uri, _ := url.Parse("kv://user:password@kvhost/0")
	opts := getKVOptions(uri)

	if opts.Password != "password" {
		t.Fail()
	}
}

func TestSkipVerifyOpt(t *testing.T) {
	uri, _ := url.Parse("kvs://kvhost/0?skipverify=true")
	tlsConfig := getKVTLSOptions(uri)

	if !tlsConfig.InsecureSkipVerify {
		t.Fail()
	}
}

func TestInsecureSkipVerifyOpt(t *testing.T) {
	uri, _ := url.Parse("kvs://kvhost/0?insecureskipverify=true")
	tlsConfig := getKVTLSOptions(uri)

	if !tlsConfig.InsecureSkipVerify {
		t.Fail()
	}
}

func TestKVSentinelUsernameOpt(t *testing.T) {
	uri, _ := url.Parse("kv+sentinel://user:password@kvhost/0?sentinelusername=suser&sentinelpassword=spass")
	opts := getKVOptions(uri).Failover()

	if opts.SentinelUsername != "suser" {
		t.Fail()
	}
}

func TestKVSentinelPasswordOpt(t *testing.T) {
	uri, _ := url.Parse("kv+sentinel://user:password@kvhost/0?sentinelusername=suser&sentinelpassword=spass")
	opts := getKVOptions(uri).Failover()

	if opts.SentinelPassword != "spass" {
		t.Fail()
	}
}

func TestKVDatabaseIndexTcp(t *testing.T) {
	uri, _ := url.Parse("kv://user:password@kvhost/12")
	opts := getKVOptions(uri)

	if opts.DB != 12 {
		t.Fail()
	}
}

func TestKVDatabaseIndexUnix(t *testing.T) {
	uri, _ := url.Parse("kv+socket:///var/run/kv.sock?database=12")
	opts := getKVOptions(uri)

	if opts.DB != 12 {
		t.Fail()
	}
}
