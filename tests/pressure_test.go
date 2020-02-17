// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxc            = 10
	debug           = false
	endpointLogin   = "http://0.0.0.0:5636/api/v1/login"
	endpointConnect = "ws://0.0.0.0:5636/api/v1/connect"
)

type jwtInput struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type jwtOutput struct {
	Code   int       `json:"code"`
	Expire time.Time `json:"expire"`
	Token  string    `json:"token"`
}

var credentials = map[string]jwtInput{
	"vnc": jwtInput{
		Protocol: "vnc",
		Host:     "172.16.238.11:5901",
		Username: "",
		Password: "vncpassword",
	},
}

func login(protocol string) string {
	credential, ok := credentials[protocol]
	if !ok {
		panic(fmt.Sprintf("login: protocol %s is not supported.", protocol))
	}

	b, err := json.Marshal(credential)
	if err != nil {
		panic(fmt.Sprintf("login: marshal credentials failed: %v", err))
	}

	resp, err := http.Post(endpointLogin, "application/json", bytes.NewReader(b))
	if err != nil {
		panic(fmt.Sprintf("login: applying credentials failed: %v", err))
	}
	defer resp.Body.Close()

	d, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(fmt.Sprintf("login: reading respons failed: %v", err))
	}

	var out jwtOutput
	err = json.Unmarshal(d, &out)
	if err != nil {
		panic(fmt.Sprintf("login: unmarshal response failed: %v", err))
	}
	return endpointConnect + "?token=" + out.Token
}

func successConnect(url string) error {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("connect: dial failed, err: %v", err)
	}
	for i := 0; i < 50; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if debug {
				fmt.Println("client: ", err)
			}
			return nil
		}
		if debug {
			fmt.Println("server: ", string(data))
		}
		if len(data) > 6 && string(data[0:6]) == "4.sync" {
			if debug {
				fmt.Println("client: ", string(data))
			}
			conn.WriteMessage(websocket.TextMessage, data)
			continue
		}
		if len(data) > 5 && string(data[0:5]) == "3.nop" {
			if debug {
				fmt.Println("client: ", string(data))
			}
			conn.WriteMessage(websocket.TextMessage, data)
			continue
		}
		if len(data) > 12 && string(data[0:12]) == "10.disconnect" {
			if debug {
				fmt.Println("client: ", string(data))
			}
			conn.WriteMessage(websocket.TextMessage, data)
			return nil
		}
	}
	conn.WriteMessage(websocket.TextMessage, []byte("10.disconnect;"))
	if debug {
		fmt.Println("client: 10.disconnect;")
	}
	return conn.Close()
}

func failConnect(url string) error {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("connect: dial failed, err: %v", err)
	}

	// read guacamole message, first message will contains the connection-id
	_, _, err = conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("error when reading message: %v", err)
	}
	conn.WriteMessage(websocket.TextMessage, []byte("bad instruction"))
	return conn.Close()
}

// TestConnectionPressure implements a pressure test for occamy service.
// It is required to run the occamy service to run this test.
//
//  make build
//  make run
//
// then run this test by:
//
//  go test -v -count=1 .
func TestConnectionPressure(t *testing.T) {
	protos := []string{
		"vnc",
	}
	connectors := map[string]func(string) error{"success": successConnect, "fail": failConnect}
	var wg sync.WaitGroup
	for i := 1; i <= maxc; i++ {
		for _, proto := range protos {
			for name, connector := range connectors {
				wg.Add(1)
				// time.Sleep(time.Second)
				go func(name string, connector func(string) error, proto string, i int) {
					defer wg.Done()

					fmt.Printf("%s-%s-%d start...\n", proto, name, i)
					err := connector(login(proto))
					if err != nil {
						fmt.Printf("%s-%s-%d err: %v\n", proto, name, i, err)
						return
					}
					fmt.Printf("%s-%s-%d done.\n", proto, name, i)
				}(name, connector, proto, i)
			}
		}
	}
	wg.Wait()
}
