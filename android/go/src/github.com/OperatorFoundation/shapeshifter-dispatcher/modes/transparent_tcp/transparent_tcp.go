/*
 * Copyright (c) 2014-2015, Yawning Angel <yawning at torproject dot org>
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions are met:
 *
 *  * Redistributions of source code must retain the above copyright notice,
 *    this list of conditions and the following disclaimer.
 *
 *  * Redistributions in binary form must reproduce the above copyright notice,
 *    this list of conditions and the following disclaimer in the documentation
 *    and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
 * AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
 * LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
 * CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
 * SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
 * INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
 * CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
 * ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 */

// Go language Tor Pluggable Transport suite.  Works only as a managed
// client/server.
package transparent_tcp

import (
	"errors"
	"fmt"
	options2 "github.com/OperatorFoundation/shapeshifter-dispatcher/common"
	"github.com/OperatorFoundation/shapeshifter-dispatcher/common/pt_extras"
	"github.com/OperatorFoundation/shapeshifter-dispatcher/transports"
	"github.com/OperatorFoundation/shapeshifter-transports/transports/Dust"
	replicant "github.com/OperatorFoundation/shapeshifter-transports/transports/Replicant"
	"github.com/OperatorFoundation/shapeshifter-transports/transports/meeklite"
	"github.com/OperatorFoundation/shapeshifter-transports/transports/obfs2"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/url"
	"sync"

	"github.com/OperatorFoundation/shapeshifter-dispatcher/common/log"
	"github.com/OperatorFoundation/shapeshifter-ipc"
	"github.com/OperatorFoundation/shapeshifter-transports/transports/obfs4"
	"github.com/OperatorFoundation/shapeshifter-transports/transports/shadow"
)

func ClientSetup(socksAddr string, target string, ptClientProxy *url.URL, names []string, options string) (launched bool) {
	// Launch each of the client listeners.
	for _, name := range names {
		ln, err := net.Listen("tcp", socksAddr)
		if err != nil {
			log.Errorf("failed to listen %s %s", name, err.Error())
			continue
		}

		go clientAcceptLoop(target, name, options, ln, ptClientProxy)
		log.Infof("%s - registered listener: %s", name, ln.Addr())
		launched = true
	}

	return
}

func clientAcceptLoop(target string, name string, options string, ln net.Listener, proxyURI *url.URL) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if e, ok := err.(net.Error); ok && !e.Temporary() {
				log.Errorf("Fatal listener error: %s", err.Error())
				return
			}
			log.Warnf("Failed to accept connection: %s", err.Error())
			continue
		}
		go clientHandler(target, name, options, conn, proxyURI)
	}
}

func clientHandler(target string, name string, options string, conn net.Conn, proxyURI *url.URL) {
	var dialer proxy.Dialer
	dialer = proxy.Direct
	if proxyURI != nil {
		var err error
		dialer, err = proxy.FromURL(proxyURI, proxy.Direct)
		if err != nil {
			// This should basically never happen, since config protocol
			// verifies this.
			fmt.Println("-> failed to obtain dialer", proxyURI, proxy.Direct)
			log.Errorf("(%s) - failed to obtain proxy dialer: %s", target, log.ElideError(err))
			return
		}
	}
	//this is where the refactoring begins
	args, argsErr := options2.ParseOptions(options)
	if argsErr != nil {
		log.Errorf("Error parsing transport options: %s", options)
		log.Errorf("Error: %s", argsErr)
		println("-> Error parsing transport options: %s", options)
		println("-> Error: %s", argsErr)
		return
	}

	// Deal with arguments.
	transport, argsToDialerErr := pt_extras.ArgsToDialer(target, name, args, dialer)
	if argsToDialerErr != nil {
		log.Errorf("Error creating a transport with the provided options: ", options)
		log.Errorf("Error: ", argsToDialerErr)
		println("-> Error creating a transport with the provided options: ", options)
		println("-> Error: ", argsToDialerErr)
		return
	}

	fmt.Println("Dialing ", target)
	remote, dialErr := transport.Dial()
	if dialErr != nil {
		println("--> Unable to dial transport server: ", dialErr.Error())
		println("-> Name: ", name)
		println("-> Options: ", options)
		log.Errorf("--> Unable to dial transport server: ", dialErr.Error())
		return
	}

	if conn == nil {
		println("--> Application connection is nil")
		log.Errorf("%s - closed connection. Application connection is nil", name)
	}

	if remote == nil {
		println("--> Transport server connection is nil.")
		log.Errorf("%s - closed connection. Transport server connection is nil", name)
	}

	if err := copyLoop(conn, remote); err != nil {
		log.Warnf("%s(%s) - closed connection: %s", name, target, log.ElideError(err))
		println("%s(%s) - closed connection: %s", name, target, log.ElideError(err))
	} else {
		log.Infof("%s(%s) - closed connection", name, target)
		println("%s(%s) - closed connection", name, target)
	}
}

func ServerSetup(ptServerInfo pt.ServerInfo, statedir string, options string) (launched bool) {
	// Launch each of the server listeners.
	for _, bindaddr := range ptServerInfo.Bindaddrs {
		name := bindaddr.MethodName

		var listen func(address string) net.Listener

		args, argsErr := options2.ParseServerOptions(options)
		if argsErr != nil {
			log.Errorf("Error parsing transport options: %s", options)
			return
		}

		// Deal with arguments.
		switch name {
		case "obfs2":
			transport := obfs2.NewObfs2Transport()
			listen = transport.Listen
		case "obfs4":
			transport, err := obfs4.NewObfs4Server(statedir)
			if err != nil {
				log.Errorf("Can't start obfs4 transport: %v", err)
				return false
			}
			listen = transport.Listen
		case "Replicant":
			shargs, aok := args["Replicant"]
			if shargs == nil {
				config := replicant.ServerConfig{
					Toneburst: nil,
					Polish:    nil,
				}
				listen = config.Listen
			} else {
				if !aok {
					println("error parsing Replicant arguments: ", shargs)
					log.Errorf("Unable to parse Replicant arguments.")
					return false
				}

				config, err := transports.ParseArgsReplicantServer(shargs)
				if err != nil {
					println("Received a Replicant config error: ", err.Error())
					log.Errorf(err.Error())
					return false
				}

				listen = config.Listen
			}
		case "Dust":
			shargs, aok := args["Dust"]
			if !aok {
				return false
			}

			untypedIdPath, ok := shargs["Url"]
			if !ok {
				return false
			}
			idPath, err := options2.CoerceToString(untypedIdPath)
			if err != nil {
				log.Errorf("could not coerce Dust Url to string")
				return false
			}
			transport := Dust.NewDustServer(idPath)
			listen = transport.Listen
		case "meeklite":
			args, aok := args["meeklite"]
			if !aok {
				return false
			}

			untypedUrl, ok := args["Url"]
			if !ok {
				return false
			}

			Url, err := options2.CoerceToString(untypedUrl)
			if err != nil {
				log.Errorf("could not coerce meeklite Url to string")
			}

			untypedFront, ok := args["front"]
			if !ok {
				return false
			}

			front, err2 := options2.CoerceToString(untypedFront)
			if err2 != nil {
				log.Errorf("could not coerce meeklite front to string")
			}
			var dialer proxy.Dialer
			transport := meeklite.NewMeekTransportWithFront(Url, front, dialer)
			listen = transport.Listen
		case "shadow":
			args, aok := args["shadow"]
			if !aok {
				return false
			}

			untypedPassword, ok := args["password"]
			if !ok {
				return false
			}

			Password, err := options2.CoerceToString(untypedPassword)
			if err != nil {
				log.Errorf("could not coerce shadow password to string")
			}

			untypedCipherNameString, ok := args["cipherName"]
			if !ok {
				return false
			}

			cipherNameString, err2 := options2.CoerceToString(untypedCipherNameString)
			if err2 != nil {
				log.Errorf("could not coerce shadow certString to string")
			}

			transport := shadow.NewShadowServer(Password, cipherNameString)
			listen = transport.Listen
		default:
			log.Errorf("Unknown transport: %s", name)
			return false
		}

		go func() {
			for {
				transportLn := listen(bindaddr.Addr.String())
				log.Infof("%s - registered listener: %s", name, log.ElideAddr(bindaddr.Addr.String()))
				serverAcceptLoop(name, transportLn, &ptServerInfo)
				transportLn.Close()
			}
		}()

		launched = true
	}

	return
}

//func getServerBindaddrs(serverBindaddr string) ([]pt.Bindaddr, error) {
//	var result []pt.Bindaddr
//
//	for _, spec := range strings.Split(serverBindaddr, ",") {
//		var bindaddr pt.Bindaddr
//
//		parts := strings.SplitN(spec, "-", 2)
//		if len(parts) != 2 {
//			log.Errorf("TOR_PT_SERVER_BINDADDR: doesn't contain \"-\" %q", spec)
//			return nil, nil
//		}
//		bindaddr.MethodName = parts[0]
//		addr, err := pt.ResolveAddr(parts[1])
//		if err != nil {
//			log.Errorf("TOR_PT_SERVER_BINDADDR: %q %q", spec, err.Error())
//			return nil, nil
//		}
//		bindaddr.Addr = addr
//		//		bindaddr.Options = optionsMap[bindaddr.MethodName]
//		result = append(result, bindaddr)
//	}
//
//	return result, nil
//}

func serverAcceptLoop(name string, ln net.Listener, info *pt.ServerInfo) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if e, ok := err.(net.Error); ok && !e.Temporary() {
				log.Errorf("Fatal listener error: %s", err.Error())
				return
			}
			log.Warnf("Failed to accept connection: %s", err.Error())
			continue
		}
		go serverHandler(name, conn, info)
	}
}

func serverHandler(name string, remote net.Conn, info *pt.ServerInfo) {
	// Connect to the orport.
	orConn, err := pt.DialOr(info, remote.RemoteAddr().String(), name)
	if err != nil {
		log.Errorf("%s - failed to connect to ORPort: %s", name, log.ElideError(err))
		return
	}

	if err = copyLoop(orConn, remote); err != nil {
		log.Warnf("%s - closed connection: %s", name, log.ElideError(err))
	} else {
		log.Infof("%s - closed connection", name)
	}
}

func copyLoop(a net.Conn, b net.Conn) error {
	println("--> Entering copy loop.")
	// Note: b is always the pt connection.  a is the SOCKS/ORPort connection.
	errChan := make(chan error, 2)

	var wg sync.WaitGroup
	wg.Add(2)

	if b == nil {
		println("--> Copy loop has a nil connection (b).")
		return errors.New("copy loop has a nil connection (b)")
	}

	if a == nil {
		println("--> Copy loop has a nil connection (a).")
		return errors.New("copy loop has a nil connection (a)")
	}

	go func() {
		defer wg.Done()
		_, err := io.Copy(b, a)
		errChan <- err
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(a, b)
		errChan <- err
	}()

	// Wait for both upstream and downstream to close.  Since one side
	// terminating closes the other, the second error in the channel will be
	// something like EINVAL (though io.Copy() will swallow EOF), so only the
	// first error is returned.
	wg.Wait()
	if len(errChan) > 0 {
		return <-errChan
	}

	return nil
}
