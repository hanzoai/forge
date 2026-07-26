// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nosql

import (
	"crypto/tls"
	"net/url"
	"path"
	"runtime/pprof"
	"strconv"
	"strings"

	"github.com/hanzoai/git/modules/log"

	"github.com/hanzokv/go/v9"
)

var replacer = strings.NewReplacer("_", "", "-", "")

// CloseKVClient closes a KV client
func (m *Manager) CloseKVClient(connection string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	client, ok := m.KVConnections[connection]
	if !ok {
		connection = ToKVURI(connection).String()
		client, ok = m.KVConnections[connection]
	}
	if !ok {
		return nil
	}

	client.count--
	if client.count > 0 {
		return nil
	}

	for _, name := range client.name {
		delete(m.KVConnections, name)
	}
	return client.UniversalClient.Close()
}

// GetKVClient gets a KV client for a particular connection
func (m *Manager) GetKVClient(connection string) (client kv.UniversalClient) {
	// Because we want associate any goroutines created by this call to the main nosqldb context we need to
	// wrap this in a goroutine labelled with the nosqldb context
	done := make(chan struct{})
	var recovered any
	go func() {
		defer func() {
			recovered = recover()
			if recovered != nil {
				log.Error("PANIC during GetKVClient: %v\nStacktrace: %s", recovered, log.Stack(2))
			}
			close(done)
		}()
		pprof.SetGoroutineLabels(m.ctx)

		client = m.getKVClient(connection)
	}()
	<-done
	if recovered != nil {
		panic(recovered)
	}
	return client
}

func (m *Manager) getKVClient(connection string) kv.UniversalClient {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	client, ok := m.KVConnections[connection]
	if ok {
		client.count++
		return client
	}

	uri := ToKVURI(connection)
	client, ok = m.KVConnections[uri.String()]
	if ok {
		client.count++
		return client
	}
	client = &kvClientHolder{
		name: []string{connection, uri.String()},
	}

	opts := getKVOptions(uri)
	tlsConfig := getKVTLSOptions(uri)

	clientName := uri.Query().Get("clientname")

	if len(clientName) > 0 {
		client.name = append(client.name, clientName)
	}

	switch uri.Scheme {
	case "redis+sentinels":
		fallthrough
	case "rediss+sentinel":
		opts.TLSConfig = tlsConfig
		fallthrough
	case "redis+sentinel":
		client.UniversalClient = kv.NewFailoverClient(opts.Failover())
	case "redis+clusters":
		fallthrough
	case "rediss+cluster":
		opts.TLSConfig = tlsConfig
		fallthrough
	case "redis+cluster":
		client.UniversalClient = kv.NewClusterClient(opts.Cluster())
	case "redis+socket":
		simpleOpts := opts.Simple()
		simpleOpts.Network = "unix"
		simpleOpts.Addr = path.Join(uri.Host, uri.Path)
		client.UniversalClient = kv.NewClient(simpleOpts)
	case "rediss":
		opts.TLSConfig = tlsConfig
		fallthrough
	case "redis":
		client.UniversalClient = kv.NewClient(opts.Simple())
	default:
		return nil
	}

	for _, name := range client.name {
		m.KVConnections[name] = client
	}

	client.count++

	return client
}

// getKVOptions pulls various configuration options based on the KV URI format and converts them to the KV client's
// UniversalOptions fields. This function explicitly excludes fields related to TLS configuration, which is
// conditionally attached to this options struct before being converted to the specific type for the redis scheme being
// used, and only in scenarios where TLS is applicable (e.g. rediss://, redis+clusters://).
func getKVOptions(uri *url.URL) *kv.UniversalOptions {
	opts := &kv.UniversalOptions{}

	// Handle username/password
	if password, ok := uri.User.Password(); ok {
		opts.Password = password
		// Username does not appear to be handled by kv.Options
		opts.Username = uri.User.Username()
	} else if uri.User.Username() != "" {
		// assume this is the password
		opts.Password = uri.User.Username()
	}

	// Now handle the uri query sets
	for k, v := range uri.Query() {
		switch replacer.Replace(strings.ToLower(k)) {
		case "addr":
			opts.Addrs = append(opts.Addrs, v...)
		case "addrs":
			opts.Addrs = append(opts.Addrs, strings.Split(v[0], ",")...)
		case "username":
			opts.Username = v[0]
		case "password":
			opts.Password = v[0]
		case "database":
			fallthrough
		case "db":
			opts.DB, _ = strconv.Atoi(v[0])
		case "maxretries":
			opts.MaxRetries, _ = strconv.Atoi(v[0])
		case "minretrybackoff":
			opts.MinRetryBackoff = valToTimeDuration(v)
		case "maxretrybackoff":
			opts.MaxRetryBackoff = valToTimeDuration(v)
		case "timeout":
			timeout := valToTimeDuration(v)
			if timeout != 0 {
				if opts.DialTimeout == 0 {
					opts.DialTimeout = timeout
				}
				if opts.ReadTimeout == 0 {
					opts.ReadTimeout = timeout
				}
			}
		case "dialtimeout":
			opts.DialTimeout = valToTimeDuration(v)
		case "readtimeout":
			opts.ReadTimeout = valToTimeDuration(v)
		case "writetimeout":
			opts.WriteTimeout = valToTimeDuration(v)
		case "poolsize":
			opts.PoolSize, _ = strconv.Atoi(v[0])
		case "minidleconns":
			opts.MinIdleConns, _ = strconv.Atoi(v[0])
		case "pooltimeout":
			opts.PoolTimeout = valToTimeDuration(v)
		case "maxredirects":
			opts.MaxRedirects, _ = strconv.Atoi(v[0])
		case "readonly":
			opts.ReadOnly, _ = strconv.ParseBool(v[0])
		case "routebylatency":
			opts.RouteByLatency, _ = strconv.ParseBool(v[0])
		case "routerandomly":
			opts.RouteRandomly, _ = strconv.ParseBool(v[0])
		case "sentinelmasterid":
			fallthrough
		case "mastername":
			opts.MasterName = v[0]
		case "sentinelusername":
			opts.SentinelUsername = v[0]
		case "sentinelpassword":
			opts.SentinelPassword = v[0]
		}
	}

	if uri.Host != "" {
		opts.Addrs = append(opts.Addrs, strings.Split(uri.Host, ",")...)
	}

	// A redis connection string uses the path section of the URI in two different ways. In a TCP-based connection, the
	// path will be a database index to automatically have the client SELECT. In a Unix socket connection, it will be the
	// file path. We only want to try to coerce this to the database index when we're not expecting a file path so that
	// the error log stays clean.
	if uri.Path != "" && uri.Scheme != "redis+socket" {
		if db, err := strconv.Atoi(uri.Path[1:]); err == nil {
			opts.DB = db
		} else {
			log.Error("Provided database identifier '%s' is not a valid integer. Hanzo Git will ignore this option.", uri.Path)
		}
	}

	return opts
}

// getKVTLSOptions parses KV URI TLS configuration parameters and converts them to the go TLS configuration
// equivalent fields.
func getKVTLSOptions(uri *url.URL) *tls.Config {
	tlsConfig := &tls.Config{}

	skipverify := uri.Query().Get("skipverify")

	if len(skipverify) > 0 {
		skipverify, err := strconv.ParseBool(skipverify)
		if err == nil {
			tlsConfig.InsecureSkipVerify = skipverify
		}
	}

	insecureskipverify := uri.Query().Get("insecureskipverify")

	if len(insecureskipverify) > 0 {
		insecureskipverify, err := strconv.ParseBool(insecureskipverify)
		if err == nil {
			tlsConfig.InsecureSkipVerify = insecureskipverify
		}
	}

	return tlsConfig
}
