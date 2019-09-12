package service

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/kushtaka/kushtakad/listener"
	"github.com/kushtaka/kushtakad/service/telnet"
	"github.com/op/go-logging"
)

var log = logging.MustGetLogger("sensors")

func configureServices(h *Hub) {
	tel := telnet.Telnet()
	sm := &ServiceMap{
		Service:    tel,
		SensorName: "unknown",
		Type:       "telnet",
		Port:       "2222",
	}

	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort("localhost", "2222"))
	if err != nil {
		log.Fatal(err)
	}

	h.ports[addr] = append(h.ports[addr], sm)

}

func startSensor(ctx context.Context) {

	h := &Hub{
		ports: make(map[net.Addr][]*ServiceMap),
	}

	configureServices(h)

	incoming := make(chan net.Conn)

	l, err := listener.NewSocket()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				log.Fatal(err)
			}

			incoming <- conn

			runtime.Gosched() // in case of goroutine starvation // with many connection and single procs
		}
	}()

	err = l.Start(ctx)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn := <-incoming:
				go h.handle(conn)
			}
		}
	}()

}

func (h *Hub) handle(c net.Conn) {
	log.Debug("handle()")

	sm, newConn, err := h.findService(c)
	if sm == nil {
		log.Debug("No suitable handler for %s => %s: %s", c.RemoteAddr(), c.LocalAddr(), err.Error())
		return
	}

	log.Debug("Handling connection for %s => %s %s(%s)", c.RemoteAddr(), c.LocalAddr(), sm.SensorName, sm.Type)

	newConn = TimeoutConn(newConn, time.Second*30)

	ctx := context.Background()
	if err := sm.Service.Handle(ctx, newConn); err != nil {
		log.Errorf(color.RedString("Error handling service: %s: %s", sm.SensorName, err.Error()))
	}
}

type Hub struct {
	mu *sync.Mutex

	// Maps a port and a protocol to an array of pointers to services
	ports map[net.Addr][]*ServiceMap
}

// Wraps a Servicer, adding some metadata
type ServiceMap struct {
	Service Servicer

	SensorName string
	Type       string
	Port       string
}

type TmpMap struct {
	Service interface{}

	SensorName string
	Type       string
	Port       string
}

type Servicer interface {
	Handle(context.Context, net.Conn) error
}

type Service struct {
	Port int
}

type Listener interface {
	Start(ctx context.Context) error
	Accept() (net.Conn, error)
}

// Addr, proto, port, error
func ToAddr(input string) (net.Addr, string, int, error) {
	parts := strings.Split(input, "/")

	if len(parts) != 2 {
		return nil, "", 0, fmt.Errorf(`wrong format (needs to be "protocol/(host:)port")`)
	}

	proto := parts[0]

	host, port, err := net.SplitHostPort(parts[1])
	if err != nil {
		port = parts[1]
	}

	portUint16, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, "", 0, fmt.Errorf("error parsing port value: %s", err.Error())
	}

	switch proto {
	case "tcp":
		addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(host, port))
		return addr, proto, int(portUint16), err
	case "udp":
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, port))
		return addr, proto, int(portUint16), err
	default:
		return nil, "", 0, fmt.Errorf("unknown protocol %s", proto)
	}
}

type CanHandlerer interface {
	CanHandle([]byte) bool
}

func (hc *Hub) findService(conn net.Conn) (*ServiceMap, net.Conn, error) {
	localAddr := conn.LocalAddr()

	var serviceCandidates []*ServiceMap

	for k, sc := range hc.ports {
		if !compareAddr(k, localAddr) {
			continue
		}

		serviceCandidates = sc
	}

	if len(serviceCandidates) == 0 {
		return nil, nil, fmt.Errorf("No service configured for the given port")
	} else if len(serviceCandidates) == 1 {
		return serviceCandidates[0], conn, nil
	}

	peekUninitialized := true
	var tConn net.Conn
	var pConn *peekConnection
	var n int
	buffer := make([]byte, 1024)
	for _, service := range serviceCandidates {
		ch, ok := service.Service.(CanHandlerer)
		if !ok {
			return service, conn, nil
		}
		if peekUninitialized {
			tConn = TimeoutConn(conn, time.Second*30)
			pConn = PeekConnection(tConn)
			log.Debug("Peeking connection %s => %s", conn.RemoteAddr(), conn.LocalAddr())
			_, err := pConn.Peek(buffer)
			if err != nil {
				return nil, nil, fmt.Errorf("could not peek bytes: %s", err.Error())
			}
			peekUninitialized = false
		}
		if ch.CanHandle(buffer[:n]) {
			return service, pConn, nil
		}
	}
	return nil, nil, fmt.Errorf("No suitable service for the given port")
}

func (h *Hub) heartbeat() {
	beat := time.Tick(30 * time.Second)
	count := 0
	for range beat {
		count++
	}
}

func compareAddr(addr1 net.Addr, addr2 net.Addr) bool {
	if ta1, ok := addr1.(*net.TCPAddr); ok {
		ta2, ok := addr2.(*net.TCPAddr)
		if !ok {
			return false
		}

		if ta1.Port != ta2.Port {
			return false
		}

		if ta1.IP == nil {
		} else if ta2.IP == nil {
		} else if !ta1.IP.Equal(ta2.IP) {
			return false
		}

		return true
	} else if ua1, ok := addr1.(*net.UDPAddr); ok {
		ua2, ok := addr2.(*net.UDPAddr)
		if !ok {
			return false
		}

		if ua1.Port != ua2.Port {
			return false
		}

		if ua1.IP == nil {
		} else if ua2.IP == nil {
		} else if !ua1.IP.Equal(ua2.IP) {
			return false
		}

		return true
	}

	return false
}

/*
func (h *Hub) ConfigureListener(ctx context.Context, Type string) listener.Listener {
	listenerFunc, ok := listener.Get(Type)
	if !ok {
		fmt.Println(color.RedString("Listener %s not support on platform", Type))
		return nil
	}

	l, err := listenerFunc()

	if err != nil {
		log.Fatalf("Error initializing listener %s: %s", Type, err)
	}

	h.ports = make(map[net.Addr][]*ServiceMap)
	for _, s := range h.config.Ports {

		x := struct {
			Port     string   `toml:"port"`
			Ports    []string `toml:"ports"`
			Services []string `toml:"services"`
		}{}

		if err := hc.config.PrimitiveDecode(s, &x); err != nil {
			log.Error("Error parsing configuration of generic ports: %s", err.Error())
			continue
		}

		var ports []string
		if x.Ports != nil {
			ports = x.Ports
		}
		if x.Port != "" {
			ports = append(ports, x.Port)
		}
		if x.Port != "" && x.Ports != nil {
			log.Warning(`Both "port" and "ports" were defined, this can be confusing`)
		} else if x.Port == "" && x.Ports == nil {
			log.Error("Neither \"port\" nor \"ports\" were defined")
			continue
		}

		if len(x.Services) == 0 {
			log.Warning("No services defined for port(s) " + strings.Join(ports, ", "))
		}

		for _, portStr := range ports {
			addr, _, _, err := ToAddr(portStr)
			if err != nil {
				log.Error("Error parsing port string: %s", err.Error())
				continue
			}
			if addr == nil {
				log.Error("Failed to bind: addr is nil")
				continue
			}

			// Get the services from their names
			var servicePtrs []*ServiceMap
			for _, serviceName := range x.Services {
				ptr, ok := hc.serviceList[serviceName]
				if !ok {
					log.Error("Unknown service '%s' for port %s", serviceName, portStr)
					continue
				}
				servicePtrs = append(servicePtrs, ptr)
				hc.isServiceUsed[serviceName] = true
			}
			if len(servicePtrs) == 0 {
				log.Errorf("Port %s has no valid services, it won't be listened on", portStr)
				continue
			}

			found := false
			for k, _ := range hc.ports {
				if !compareAddr(k, addr) {
					continue
				}

				found = true
			}

			if found {
				log.Error("Port %s was already defined, ignoring the newer definition", portStr)
				continue
			}

			hc.ports[addr] = servicePtrs

			a, ok := l.(listener.AddAddresser)
			if !ok {
				log.Error("Listener error")
				continue
			}
			a.AddAddress(addr)

			log.Infof("Configured port %s/%s", addr.Network(), addr.String())
		}
	}

	for name, isUsed := range hc.isServiceUsed {
		if !isUsed {
			log.Warningf("Service %s is defined but not used", name)
		}
	}

	if len(hc.config.Undecoded()) != 0 {
		log.Warningf("Unrecognized keys in configuration: %v", hc.config.Undecoded())
	}

	if err := l.Start(ctx); err != nil {
		fmt.Println(color.RedString("Error starting listener: %s", err.Error()))
	}

	return l
}

*/
