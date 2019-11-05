package tests

import (
	"fmt"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

const (
	maxc   = 10
	urlVNC = "ws://0.0.0.0:5636/api/v1/connect?token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1NzI5NTUwMzUsImhvc3QiOiIxNzIuMTYuMjM4LjExOjU5MDEiLCJvcmlnX2lhdCI6MTU3Mjk1MTQzNSwicGFzc3dvcmQiOiJ2bmNwYXNzd29yZCIsInByb3RvY29sIjoidm5jIiwidXNlcm5hbWUiOiIifQ.TM0-yt5kGmoBSvzMzmJXiear7rDVKdMewcxdccmm9Zs"
	urlRDP = "ws://0.0.0.0:5636/api/v1/connect?token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1NzI5NTc5MzgsImhvc3QiOiIxNzIuMTYuMjM4LjEyOjMzODkiLCJvcmlnX2lhdCI6MTU3Mjk1NDMzOCwicGFzc3dvcmQiOiJEb2NrZXIiLCJwcm90b2NvbCI6InJkcCIsInVzZXJuYW1lIjoicm9vdCJ9.FISbcjJ2J8hUpFphIvjE9C-PFSzEuVSPapll1BKvnNU"
	urlSSH = "ws://0.0.0.0:5636/api/v1/connect?token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE1NzI5NTY4MzUsImhvc3QiOiIxNzIuMTYuMjM4LjEzOjIyIiwib3JpZ19pYXQiOjE1NzI5NTMyMzUsInBhc3N3b3JkIjoicm9vdCIsInByb3RvY29sIjoic3NoIiwidXNlcm5hbWUiOiJyb290In0.JxyTAxWPuXBnvBsU0aqR75sskk8KpB9kQPFhoUNL7RQ"
	debug  = false
)

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
	protos := []string{"vnc", "rdp", "ssh"}
	connectors := []func(string) error{successConnect, failConnect}
	var wg sync.WaitGroup
	for i := 1; i <= maxc; i++ {
		for _, proto := range protos {
			for _, connector := range connectors {
				wg.Add(1)
				go func(connector func(string) error, proto string, i int) {
					fmt.Printf("%s-%d start...\n", proto, i)
					var url string
					switch proto {
					case "vnc":
						url = urlVNC
					case "rdp":
						url = urlRDP
					case "ssh":
						url = urlSSH
					}
					err := connector(url)
					if err != nil {
						fmt.Println("err: ", err)
					}

					fmt.Printf("%s-%d done.\n", proto, i)
					wg.Done()
				}(connector, proto, i)
			}
		}
	}
	wg.Wait()
}
