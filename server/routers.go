package server

import (
	"net/http"

	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/changkun/occamy/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Ping implements /api/v1/ping
func (p *proxy) Ping(c *gin.Context) {
	c.JSON(http.StatusOK, struct {
		Version   string `json:"version"`
		BuildTime string `json:"build_time"`
		GitCommit string `json:"git_commit"`
	}{
		Version:   config.Version,
		GitCommit: config.GitCommit,
		BuildTime: config.BuildTime,
	})
}

// serveWS implements /api/v1/connect
func (p *proxy) serveWS(c *gin.Context) {
	ws, err := p.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logrus.Errorf("occamy-proxy: upgrade websocket failed: %v", err)
		c.Writer.Write([]byte(http.StatusText(http.StatusBadRequest)))
		return
	}

	claims := jwt.ExtractClaims(c)
	jwt := &config.JWT{
		Protocol: claims["protocol"].(string),
		Host:     claims["host"].(string),
		Username: claims["username"].(string),
		Password: claims["password"].(string),
	}
	err = p.routeConn(ws, jwt)
	if err != nil {
		logrus.Errorf("occamy-proxy: route connection failed: %v", err)
		ws.WriteMessage(websocket.CloseMessage, []byte(err.Error()))
	}
	ws.Close()
}

func (p *proxy) routeConn(ws *websocket.Conn, jwt *config.JWT) (err error) {
	p.mu.Lock()
	s, ok := p.sessions[jwt.GenerateID()]
	if ok {
		err = s.Join(ws, jwt, false, func() { p.mu.Unlock() })
		return
	}

	s, err = NewSession(jwt.Protocol)
	if err != nil {
		p.mu.Unlock()
		return
	}

	p.sessions[jwt.GenerateID()] = s
	logrus.Infof("occamy-proxy: new session was created: %s", s.ID)
	err = s.Join(ws, jwt, true, func() { p.mu.Unlock() }) // block here

	p.mu.Lock()
	delete(p.sessions, jwt.GenerateID())
	p.mu.Unlock()
	return
}
