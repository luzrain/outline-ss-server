// Copyright 2018 Jigsaw Operations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"container/list"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Jigsaw-Code/outline-ss-server/service"
	"github.com/Jigsaw-Code/outline-ss-server/service/metrics"
	ss "github.com/Jigsaw-Code/outline-ss-server/shadowsocks"
	"github.com/op/go-logging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/crypto/ssh/terminal"
	"gopkg.in/yaml.v2"
)

var logger *logging.Logger

// Set by goreleaser default ldflags. See https://goreleaser.com/customization/build/
var version = "dev"

var server *SSServer

// 59 seconds is most common timeout for servers that do not respond to invalid requests
const tcpReadTimeout time.Duration = 59 * time.Second

// A UDP NAT timeout of at least 5 minutes is recommended in RFC 4787 Section 4.3.
const defaultNatTimeout time.Duration = 5 * time.Minute

func init() {
	var prefix = "%{level:.1s}%{time:2006-01-02T15:04:05.000Z07:00} %{pid} %{shortfile}]"
	if terminal.IsTerminal(int(os.Stderr.Fd())) {
		// Add color only if the output is the terminal
		prefix = strings.Join([]string{"%{color}", prefix, "%{color:reset}"}, "")
	}
	logging.SetFormatter(logging.MustStringFormatter(strings.Join([]string{prefix, " %{message}"}, "")))
	logging.SetBackend(logging.NewLogBackend(os.Stderr, "", 0))
	logger = logging.MustGetLogger("")
}

type ssPort struct {
	tcpService service.TCPService
	udpService service.UDPService
	cipherList service.CipherList
}

type SSServer struct {
	natTimeout  time.Duration
	m           metrics.ShadowsocksMetrics
	replayCache service.ReplayCache
	ports       map[int]*ssPort
}

func (s *SSServer) startPort(portNum int) error {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: portNum})
	if err != nil {
		return fmt.Errorf("Failed to start TCP on port %v: %v", portNum, err)
	}
	packetConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: portNum})
	if err != nil {
		return fmt.Errorf("Failed to start UDP on port %v: %v", portNum, err)
	}
	logger.Infof("Listening TCP and UDP on port %v", portNum)
	port := &ssPort{cipherList: service.NewCipherList()}
	// TODO: Register initial data metrics at zero.
	port.tcpService = service.NewTCPService(port.cipherList, &s.replayCache, s.m, tcpReadTimeout)
	port.udpService = service.NewUDPService(s.natTimeout, port.cipherList, s.m)
	s.ports[portNum] = port
	go port.tcpService.Serve(listener)
	go port.udpService.Serve(packetConn)
	return nil
}

func (s *SSServer) removePort(portNum int) error {
	port, ok := s.ports[portNum]
	if !ok {
		return fmt.Errorf("Port %v doesn't exist", portNum)
	}
	tcpErr := port.tcpService.Stop()
	udpErr := port.udpService.Stop()
	delete(s.ports, portNum)
	if tcpErr != nil {
		return fmt.Errorf("Failed to close listener on %v: %v", portNum, tcpErr)
	}
	if udpErr != nil {
		return fmt.Errorf("Failed to close packetConn on %v: %v", portNum, udpErr)
	}
	logger.Infof("Stopped TCP and UDP on port %v", portNum)
	return nil
}

func (s *SSServer) loadConfig(config *Config) error {
	portChanges := make(map[int]int)
	portCiphers := make(map[int]*list.List) // Values are *List of *CipherEntry.
	for _, keyConfig := range config.Keys {
		portChanges[keyConfig.Port] = 1
		cipherList, ok := portCiphers[keyConfig.Port]
		if !ok {
			cipherList = list.New()
			portCiphers[keyConfig.Port] = cipherList
		}
		cipher, err := ss.NewCipher(keyConfig.Cipher, keyConfig.Secret)
		if err != nil {
			return fmt.Errorf("Failed to create cipher for key %v: %v", keyConfig.ID, err)
		}
		entry := service.MakeCipherEntry(keyConfig.ID, cipher, keyConfig.Secret)
		cipherList.PushBack(&entry)
	}
	for port := range s.ports {
		portChanges[port] = portChanges[port] - 1
	}
	for portNum, count := range portChanges {
		if count == -1 {
			if err := s.removePort(portNum); err != nil {
				return fmt.Errorf("Failed to remove port %v: %v", portNum, err)
			}
		} else if count == +1 {
			if err := s.startPort(portNum); err != nil {
				return fmt.Errorf("Failed to start port %v: %v", portNum, err)
			}
		}
	}
	for portNum, cipherList := range portCiphers {
		s.ports[portNum].cipherList.Update(cipherList)
	}
	logger.Infof("Loaded %v access keys", len(config.Keys))
	s.m.SetNumAccessKeys(len(config.Keys), len(portCiphers))
	return nil
}

func (s *SSServer) loadConfigFile(filename string) error {
	config := Config{}
	configData, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(configData, &config)
	if err != nil {
		return fmt.Errorf("Failed to read config file %v: %v", filename, err)
	}
	return s.loadConfig(&config)
}

// Stop serving on all ports.
func (s *SSServer) Stop() error {
	for portNum := range s.ports {
		if err := s.removePort(portNum); err != nil {
			return err
		}
	}
	return nil
}

// RunSSServer starts a shadowsocks server running, and returns the server or an error.
func RunSSServer(filename string, natTimeout time.Duration, sm metrics.ShadowsocksMetrics, replayHistory int) (*SSServer, error) {
	server := &SSServer{
		natTimeout:  natTimeout,
		m:           sm,
		replayCache: service.NewReplayCache(replayHistory),
		ports:       make(map[int]*ssPort),
	}
	err := server.loadConfigFile(filename)
	if err != nil {
		return nil, fmt.Errorf("Failed to load config file %v: %v", filename, err)
	}
	sigHup := make(chan os.Signal, 1)
	signal.Notify(sigHup, syscall.SIGHUP)
	go func() {
		for range sigHup {
			logger.Info("Updating config")
			if err := server.loadConfigFile(filename); err != nil {
				logger.Errorf("Could not reload config: %v", err)
			}
		}
	}()
	return server, nil
}

var flags struct {
	ConfigFile    string
	ListenAddress string
	natTimeout    time.Duration
	replayHistory int
	Verbose       bool
	Version       bool
}

type Config struct {
	Keys []struct {
		ID     string
		Port   int
		Cipher string
		Secret string
	}
}

func main() {
	flag.StringVar(&flags.ConfigFile, "config", "", "Configuration filename")
	flag.StringVar(&flags.ListenAddress, "web.listen", "0.0.0.0:8080", "Address for the Prometheus metrics")
	flag.DurationVar(&flags.natTimeout, "udptimeout", defaultNatTimeout, "UDP tunnel timeout")
	flag.IntVar(&flags.replayHistory, "replay_history", 0, "Replay buffer size (# of handshakes)")
	flag.BoolVar(&flags.Verbose, "verbose", false, "Enables verbose logging output")
	flag.BoolVar(&flags.Version, "version", false, "The version of the server")

	flag.Parse()

	if flags.Verbose {
		logging.SetLevel(logging.DEBUG, "")
	} else {
		logging.SetLevel(logging.INFO, "")
	}

	if flags.Version {
		fmt.Println(version)
		return
	}

	if flags.ConfigFile == "" {
		flag.Usage()
		return
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/secrets", LoadSecretsHandler)
	http.HandleFunc("/reset", ResetHandler)

	go func() {
		logger.Fatal(http.ListenAndServe(flags.ListenAddress, nil))
	}()
	logger.Infof("Api server has started on http://%v/", flags.ListenAddress)

	var err error
	m := metrics.NewPrometheusShadowsocksMetrics(prometheus.DefaultRegisterer)
	m.SetBuildInfo(version)
	server, err = RunSSServer(flags.ConfigFile, flags.natTimeout, m, flags.replayHistory)
	if err != nil {
		logger.Fatal(err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

func LoadSecretsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	logger.Info("Updating config")
	var err error
	var jsonConfig Config
	err = json.NewDecoder(r.Body).Decode(&jsonConfig)
	if err != nil {
		w.Write([]byte(fmt.Sprintf(`{"success":false,"error":"%v"}`, err)))
		logger.Errorf("%s", err.Error())
		return
	}
	err = server.loadConfig(&jsonConfig)
	if err != nil {
		w.Write([]byte(fmt.Sprintf(`{"success":false,"error":"%v"}`, err)))
		logger.Errorf("%s", err.Error())
		return
	}
	configByteArray, _ := yaml.Marshal(jsonConfig)
	err = ioutil.WriteFile(flags.ConfigFile, configByteArray, 0644)
	if err != nil {
		w.Write([]byte(fmt.Sprintf(`{"success":false,"error":"%v"}`, err)))
		logger.Errorf("%s", err.Error())
		return
	}
	w.Write([]byte(fmt.Sprintf(`{"success":true,"response":"Loaded %v access keys"}`, len(jsonConfig.Keys))))
}

func ResetHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true,"response":"ok"}`))
	go reset()
}

func reset() {
	time.Sleep(100 * time.Millisecond)
	logger.Info("Server has resetted")
	os.Exit(1)
}
