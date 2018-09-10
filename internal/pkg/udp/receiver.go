/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/

package udp

import (
	"context"
	"log"
	"net"
	"os"

	"collectd.org/api"
	"collectd.org/network"
	"github.com/prometheus/client_golang/prometheus"
)

// Usage and command-line flags
/*func usage() {
	fmt.Fprintf(os.Stderr, `Usage: %s url [url ...]
Receive messages from all URLs concurrently and print them.
URLs are of the form "amqp://<host>:<port>/<amqp-address>"
`, os.Args[0])
	flag.PrintDefaults()
}*/

var debugr = func(format string, data ...interface{}) {} // Default no debugging output

//UDPServer msgcount -1 is infinite
type UDPServer struct {
	address     string
	debug       bool
	msgcount    int
	notifier    chan string
	status      chan int
	done        chan bool
	writer      api.Writer
	typesdbfile string
	buffer      int
	udpHandler  *UDPHandler
	srv         network.Server
	ctx         context.Context
}

//UDPHandler ...
type UDPHandler struct {
	totalCount         int64
	totalProcessed     int64
	totalCountDesc     *prometheus.Desc
	totalProcessedDesc *prometheus.Desc
}

//NewUDPServer   ...
func NewUDPServer(urlStr string, debug bool, msgcount int, udpHandler *UDPHandler, typesdbfile string, done chan bool) *UDPServer {
	if len(urlStr) == 0 {
		log.Println("No address provided")
		//usage()
		os.Exit(1)
	}
	log.Printf("geher %d", udpHandler.GetTotalMsgRcv())

	var server *UDPServer
	server = &UDPServer{
		address:     urlStr,
		debug:       debug,
		notifier:    make(chan string),
		status:      make(chan int),
		msgcount:    msgcount,
		done:        done,
		typesdbfile: typesdbfile,
		udpHandler:  udpHandler,
		ctx:         context.Background(),
		buffer:      1024,
	}
	server.writer = server

	if debug {
		debugr = func(format string, data ...interface{}) {
			log.Printf(format, data...)
		}
	}
	// Spawn off the server's main loop immediately
	// not exported
	go server.start()

	return server
}

//GetUDPHandler  ...
func (s *UDPServer) GetUDPHandler() *UDPHandler {
	return s.udpHandler
}

func (s *UDPServer) Write(_ context.Context, vl *api.ValueList) error {
	jsonStringByte, er := vl.MarshalJSON()
	jsonString := "[" + string(jsonStringByte[0:]) + "]"
	s.udpHandler.IncTotalMsgRcv()
	debugr("Debug: Getting message from UDP%#v\n", jsonString)
	debugr("Debug: Sending message to Notifier channel")
	s.notifier <- jsonString
	return er

}

//NewUDPHandler  ...
func NewUDPHandler(source string) *UDPHandler {
	plabels := prometheus.Labels{}
	plabels["source"] = source
	return &UDPHandler{
		totalCount:     0,
		totalProcessed: 0,
		totalCountDesc: prometheus.NewDesc("sa_collectd_total_udp_message_recv_count",
			"Total count of udp message received.",
			nil, plabels,
		),
		totalProcessedDesc: prometheus.NewDesc("sa_collectd_total_udp_processed_message_count",
			"Total count of udp message processed.",
			nil, plabels,
		),
	}
}

//IncTotalMsgRcv ...
func (a *UDPHandler) IncTotalMsgRcv() {
	a.totalCount++
}

//IncTotalMsgProcessed ...
func (a *UDPHandler) IncTotalMsgProcessed() {
	a.totalProcessed++
}

//GetTotalMsgRcv ...
func (a *UDPHandler) GetTotalMsgRcv() int64 {
	return a.totalCount
}

//GetTotalMsgProcessed ...
func (a *UDPHandler) GetTotalMsgProcessed() int64 {
	return a.totalProcessed
}

//Describe ...
func (a *UDPHandler) Describe(ch chan<- *prometheus.Desc) {

	ch <- a.totalCountDesc
	ch <- a.totalProcessedDesc
}

//Collect implements prometheus.Collector.
func (a *UDPHandler) Collect(ch chan<- prometheus.Metric) {

	ch <- prometheus.MustNewConstMetric(a.totalCountDesc, prometheus.CounterValue, float64(a.totalCount))
	ch <- prometheus.MustNewConstMetric(a.totalProcessedDesc, prometheus.CounterValue, float64(a.totalProcessed))

}

//GetNotifier  Get notifier
func (s *UDPServer) GetNotifier() chan string {
	return s.notifier
}

//GetStatus  Get Status
func (s *UDPServer) GetStatus() chan int {
	return s.status
}

//Close connections it is exported so users can force close
func (s *UDPServer) Close() {
	s.srv.Conn.Close()
}

//start  starts amqp server
func (s *UDPServer) start() {
	s.srv = network.Server{
		Addr:   s.address,
		Writer: s.writer,
	}
	s.srv.SecurityLevel = network.None

	if s.typesdbfile != "" {
		file, err := os.Open(s.typesdbfile)
		if err != nil {
			log.Fatalf("Can't open types.db file %s", s.typesdbfile)
		}
		defer file.Close()

		typesDB, err := api.NewTypesDB(file)
		if err != nil {
			log.Fatalf("Error in parsing types.db file %s", s.typesdbfile)
		}
		s.srv.TypesDB = typesDB
	}
	laddr, err := net.ResolveUDPAddr("udp", s.address)
	if err != nil {
		log.Fatalf("Failed to resolve binary protocol listening UDP address %q: %v", s.address, err)
	}

	if laddr.IP != nil && laddr.IP.IsMulticast() {
		s.srv.Conn, err = net.ListenMulticastUDP("udp", nil, laddr)
	} else {
		s.srv.Conn, err = net.ListenUDP("udp", laddr)
	}
	if err != nil {
		log.Fatalf("Failed to create a socket for a binary protocol server: %v", err)
	}
	if s.buffer > 0 {
		if err = s.srv.Conn.SetReadBuffer(s.buffer); err != nil {
			log.Fatalf("Failed to adjust a read buffer of the socket: %v", err)
		}
	}

	go func() {
		log.Fatal(s.srv.ListenAndWrite(s.ctx))
	}()
}

func fatalIf(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
