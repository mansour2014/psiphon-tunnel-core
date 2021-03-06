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
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DialConfig contains parameters to determine the behavior
// of a Psiphon dialer (TCPDial, MeekDial, etc.)
type DialConfig struct {

	// UpstreamHttpProxyAddress specifies an HTTP proxy to connect through
	// (the proxy must support HTTP CONNECT). The address may be a hostname
	// or IP address and must include a port number.
	UpstreamHttpProxyAddress string

	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration

	// PendingConns is used to interrupt dials in progress.
	// The dial may be interrupted using PendingConns.CloseAll(): on platforms
	// that support this, the new conn is added to pendingConns before the network
	// connect begins and removed from pendingConns once the connect succeeds or fails.
	PendingConns *Conns

	// BindToDevice parameters are used to exclude connections and
	// associated DNS requests from VPN routing.
	// When BindToDeviceProvider is set, any underlying socket is
	// submitted to the device binding servicebefore connecting.
	// The service should bind the socket to a device so that it doesn't route
	// through a VPN interface. This service is also used to bind UDP sockets used
	// for DNS requests, in which case BindToDeviceDnsServer is used as the
	// DNS server.
	BindToDeviceProvider  DeviceBinder
	BindToDeviceDnsServer string
}

// DeviceBinder defines the interface to the external BindToDevice provider
type DeviceBinder interface {
	// TODO: return 'error'; currently no return value due to
	// Android Library limitation.
	BindToDevice(fileDescriptor int)
}

// Dialer is a custom dialer compatible with http.Transport.Dial.
type Dialer func(string, string) (net.Conn, error)

// Conn is a net.Conn which supports sending a signal to a channel when
// it is closed. In Psiphon, this interface is implemented by tunnel
// connection types (DirectConn and MeekConn) and the close signal is
// used as one trigger for tearing down the tunnel.
type Conn interface {
	net.Conn

	// SetClosedSignal sets the channel which will be signaled
	// when the connection is closed. This function returns false
	// if the connection is already closed (and would never send
	// the signal). SetClosedSignal and Close may be called by
	// concurrent goroutines.
	SetClosedSignal(closedSignal chan struct{}) bool
}

// Conns is a synchronized list of Conns that is used to coordinate
// interrupting a set of goroutines establishing connections, or
// close a set of open connections, etc.
// Once the list is closed, no more items may be added to the
// list (unless it is reset).
type Conns struct {
	mutex    sync.Mutex
	isClosed bool
	conns    map[net.Conn]bool
}

func (conns *Conns) Reset() {
	conns.mutex.Lock()
	defer conns.mutex.Unlock()
	conns.isClosed = false
	conns.conns = make(map[net.Conn]bool)
}

func (conns *Conns) Add(conn net.Conn) bool {
	conns.mutex.Lock()
	defer conns.mutex.Unlock()
	if conns.isClosed {
		return false
	}
	if conns.conns == nil {
		conns.conns = make(map[net.Conn]bool)
	}
	conns.conns[conn] = true
	return true
}

func (conns *Conns) Remove(conn net.Conn) {
	conns.mutex.Lock()
	defer conns.mutex.Unlock()
	delete(conns.conns, conn)
}

func (conns *Conns) CloseAll() {
	conns.mutex.Lock()
	defer conns.mutex.Unlock()
	conns.isClosed = true
	for conn, _ := range conns.conns {
		conn.Close()
	}
	conns.conns = make(map[net.Conn]bool)
}

// Relay sends to remoteConn bytes received from localConn,
// and sends to localConn bytes received from remoteConn.
func Relay(localConn, remoteConn net.Conn) {
	copyWaitGroup := new(sync.WaitGroup)
	copyWaitGroup.Add(1)
	go func() {
		defer copyWaitGroup.Done()
		_, err := io.Copy(localConn, remoteConn)
		if err != nil {
			NoticeAlert("Relay failed: %s", ContextError(err))
		}
	}()
	_, err := io.Copy(remoteConn, localConn)
	if err != nil {
		NoticeAlert("Relay failed: %s", ContextError(err))
	}
	copyWaitGroup.Wait()
}

// HttpProxyConnect establishes a HTTP CONNECT tunnel to addr through
// an established network connection to an HTTP proxy. It is assumed that
// no payload bytes have been sent through the connection to the proxy.
func HttpProxyConnect(rawConn net.Conn, addr string) (err error) {
	hostname, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ContextError(err)
	}

	// TODO: use the proxy request/response code from net/http/transport.go?
	connectRequest := fmt.Sprintf(
		"CONNECT %s HTTP/1.1\r\nHost: %s\r\nConnection: Keep-Alive\r\n\r\n",
		addr, hostname)
	_, err = rawConn.Write([]byte(connectRequest))
	if err != nil {
		return ContextError(err)
	}

	// Adapted from dialConn in net/http/transport.go:
	// Read response.
	// Okay to use and discard buffered reader here, because
	// TLS server will not speak until spoken to.
	response, err := http.ReadResponse(bufio.NewReader(rawConn), nil)
	if err != nil {
		return ContextError(err)
	}
	if response.StatusCode != 200 {
		return ContextError(errors.New(strings.SplitN(response.Status, " ", 2)[1]))
	}

	return nil
}
