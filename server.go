package mudoo

import (
    "log"
    "net"
    "sync"
    "sync/atomic"
    "time"

    "github.com/golang/protobuf/proto"
)

type Server struct {
    fd           uint32           // Holds the fd of conns.
    config       Config           // Holds the configuration values.
    addr         *net.TCPAddr     // Listen on port.
    listener     *net.TCPListener // Holds the listener.
    sessions     map[uint32]*Conn // Holds the outstanding conns.
    sessionsLock *sync.RWMutex    // Protects the conns.
    codec        *GPBCodec

    callbacks struct {
        onConnect    func(*Conn) // Invoked on new connection.
        onDisconnect func(*Conn) // Invoked on a lost connection.
    }
}

// NewServer creates a new socket server with chosen configuration
// options. If config is nil, the DefaultConfig is used.
func NewServer(config *Config) *Server {
    if config == nil {
        config = &DefaultConfig
    }

    tcpAddr, err := net.ResolveTCPAddr("tcp4", config.ListenAddr)
    if err != nil {
        return nil
    }

    serv := &Server{
        config:       *config,
        addr:         tcpAddr,
        sessions:     make(map[uint32]*Conn),
        sessionsLock: new(sync.RWMutex),
        codec:        NewGPBCodec(),
    }

    return serv
}

func (serv *Server) Run() {
    ln, err := net.ListenTCP("tcp", serv.addr)
    if err != nil {
        serv.Log("mudoo/Run: bind tcp port failure:", err)
        return
    }

    serv.listener = ln

    var tempDelay time.Duration // how long to sleep on accept failure
    for {
        co, err := ln.AcceptTCP()
        if err != nil {
            if ne, ok := err.(net.Error); ok && ne.Temporary() {
                if tempDelay == 0 {
                    tempDelay = 5 * time.Millisecond
                } else {
                    tempDelay *= 2
                }
                if max := 1 * time.Second; tempDelay > max {
                    tempDelay = max
                }
                log.Printf("mudoo: Accept error: %v; retrying in %v", err, tempDelay)
                time.Sleep(tempDelay)
                continue
            }
            return
        }
        tempDelay = 0

        fd := atomic.AddUint32(&serv.fd, 1)
        c, err := newConn(serv, fd, co)
        if err != nil {
            return
        }
        serv.doConnect(c)
    }
}

// Broadcast schedules data to be sent to each connection.
func (serv *Server) Broadcast(data Message) {
    serv.BroadcastExcept(nil, data)
}

// BroadcastExcept schedules data to be sent to each connection except
// c. Argument data must be Message type
func (serv *Server) BroadcastExcept(c *Conn, data Message) {
    serv.sessionsLock.RLock()
    defer serv.sessionsLock.RUnlock()

    for _, v := range serv.sessions {
        if v != c {
            v.Send(data)
        }
    }
}

// GetConn digs for a conn with fd and returns it.
func (serv *Server) GetConn(sessid uint32) (c *Conn) {
    serv.sessionsLock.RLock()
    c = serv.sessions[sessid]
    serv.sessionsLock.RUnlock()
    return
}

// OnConnect sets f to be invoked when a new connection is established. It passes
// the established connection as an argument to the callback.
func (serv *Server) OnConnect(f func(*Conn)) error {
    serv.callbacks.onConnect = f
    return nil
}

// OnDisconnect sets f to be invoked when a connection is considered to be lost.
// It passes the established connection as an argument to the callback. After
// disconnection the connection is considered to be destroyed, and it should not
// be used anymore.
func (serv *Server) OnDisconnect(f func(*Conn)) error {
    serv.callbacks.onDisconnect = f
    return nil
}

// OnMessage sets f to be invoked when a message arrives. It passes the
// established connection along with the received message as arguments
// to the callback.
func (serv *Server) OnMessage(protoid uint16, prototype proto.Message, callback func(*Conn, proto.Message)) error {
    codec := serv.config.Codec
    codec.RegisterCallback(protoid, prototype, callback)
    return nil
}

func (serv *Server) Log(v ...interface{}) {
    if logger := serv.config.Logger; logger != nil {
        logger.Println(v...)
    }
}

func (serv *Server) Logf(format string, v ...interface{}) {
    if logger := serv.config.Logger; logger != nil {
        logger.Printf(format, v...)
    }
}

// OnConnect is invoked by a connection when a new connection has been
// established successfully. The establised connection is passed as an
// argument. It stores the connection and calls the user's OnConnect callback.
func (serv *Server) doConnect(c *Conn) {
    serv.sessionsLock.Lock()
    serv.sessions[c.fd] = c
    serv.sessionsLock.Unlock()

    if fn := serv.callbacks.onConnect; fn != nil {
        fn(c)
    }
}

// OnDisconnect is invoked by a connection when the connection is considered
// to be lost. It removes the connection and calls the user's OnDisconnect callback.
func (serv *Server) doDisconnect(c *Conn) {
    serv.sessionsLock.Lock()
    delete(serv.sessions, c.fd)
    serv.sessionsLock.Unlock()

    if fn := serv.callbacks.onDisconnect; fn != nil {
        fn(c)
    }
}
