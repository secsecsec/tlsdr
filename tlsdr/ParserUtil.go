package main

import (
	"container/list"
	_ "errors"
	"fmt"
	"github.com/rahulsom/TLSHandshakeDecoder"
	_ "github.com/davecgh/go-spew/spew"
	"github.com/google/gopacket"
	"github.com/google/gopacket/pcap"
	"log"
)

//TODO obviously not a main function, rename it to the caller
func parseFile(fileName string) list.List {
	connections := list.List{}
	if handle, err := pcap.OpenOffline(fileName); err != nil {
		panic(err)
	} else {
		packetSource := gopacket.NewPacketSource(handle, handle.LinkType())
		processPacketsChan(packetSource.Packets(), &connections)
	}
	return connections
}

func connectionIdentifier(tcpContent []byte, ipContent []byte) (string, bool, string, string) {
	srcPort := uint16(tcpContent[0])<<8 | uint16(tcpContent[1])
	destPort := uint16(tcpContent[2])<<8 | uint16(tcpContent[3])
	srcIp := fmt.Sprintf("%d.%d.%d.%d", ipContent[12], ipContent[13], ipContent[14], ipContent[15])
	destIp := fmt.Sprintf("%d.%d.%d.%d", ipContent[16], ipContent[17], ipContent[18], ipContent[19])

	if srcPort < destPort {
		return fmt.Sprintf("%s-%d-%s-%d", destIp, destPort, srcIp, srcPort), false, destIp, fmt.Sprintf("%s:%d", srcIp, srcPort)
	} else {
		return fmt.Sprintf("%s-%d-%s-%d", srcIp, srcPort, destIp, destPort), true, srcIp, fmt.Sprintf("%s:%d", destIp, destPort)
	}
}

// chanPacs: raw data as channel of gopacket.Packet from pcap file
// return: a list of packets that has payload([]byte)
func processPacketsChan(chanPacs chan gopacket.Packet, connections *list.List) {
	connMap := make(map[string]*Connection)

	for packet := range chanPacs {
		if packet.ApplicationLayer() != nil {
			ipContent := packet.NetworkLayer().LayerContents()
			tcpContent := packet.TransportLayer().LayerContents()
			tlsPayload := packet.ApplicationLayer().Payload()
			cId, clientSent, from, to := connectionIdentifier(tcpContent, ipContent)

			connection := connMap[cId]

			if connection == nil {
				c1 := NewConnection(from, to)
				c1.ConnectionId = cId
				//log.Printf("Addr 1 %T", c1)
				connection = c1
				connMap[cId] = connection
				connections.PushBack(c1)
			}

			plList := list.List{}
			plList.PushBack(tlsPayload)
			// tlsPayload := packet.ApplicationLayer().Payload()
			handshakePackets := produceHandshakePackets(plList)
			alertPackets := ProduceAlertPackets(plList)
			for e := alertPackets.Front(); e != nil; e = e.Next() {
				alert := e.Value.(Alert)
				DetectProblem(connection, int(alert.Description))
			}
			events := CreateEventsFromHSPackets(handshakePackets, clientSent)
			for e := events.Front(); e != nil; e = e.Next() {
				connection.AddEvent(e.Value.(*Event))
			}

		}
	}
}

// payloadPacs: a list of raw packets([]byte)
// return a list of TLSRecordLayer that only contains handshake packets
func produceHandshakePackets(payloadPacs list.List) list.List {
	var handShakePacs list.List
	for e := payloadPacs.Front(); e != nil; e = e.Next() {
		//var p TLSHandshakeDecoder.TLSRecordLayer
		tlsPayload := e.Value.([]byte)
		packets := DecomposeRecordLayer(tlsPayload)
		for e := packets.Front(); e != nil; e = e.Next() {
			if e.Value.(TLSHandshakeDecoder.TLSRecordLayer).ContentType == TLSHandshakeDecoder.TypeHandshake {
				handShakePacs.PushBack(e.Value)
			}
			//log.Println(e)
		}
	}

	//
	//log.Printf("%04x", TLSHandshakeDecoder.VersionTLS10)
	return handShakePacs
}

func getHandShakeSegment(p TLSHandshakeDecoder.TLSRecordLayer) TLSHandshakeDecoder.TLSHandshake {
	var ph TLSHandshakeDecoder.TLSHandshake
	err := TLSHandshakeDecoder.TLSDecodeHandshake(&ph, p.Fragment)
	if err != nil {
		panic(err)
	} else {
		//log.Println("Parsed Handshake data:", ph)
		return ph
	}
}

//parse a handshake to a client hello struct
func parseClientHello(hsp TLSHandshakeDecoder.TLSHandshake) TLSHandshakeDecoder.TLSClientHello {
	var pch TLSHandshakeDecoder.TLSClientHello
	err := TLSHandshakeDecoder.TLSDecodeClientHello(&pch, hsp.Body)
	if err != nil {
		panic(err)
	} else {
		log.Println("Parsed Client Hello data: ", pch)
		return pch
	}
}

func CreateEventsFromHSPackets(handShakePacs list.List, clientSent bool) list.List {
	var events list.List
	for el := handShakePacs.Front(); el != nil; el = el.Next() {
		tlsRecordLayer := el.Value.(TLSHandshakeDecoder.TLSRecordLayer)
		hsPackets := DecomposeHandshakes(tlsRecordLayer.Fragment)
		for e := hsPackets.Front(); e != nil; e = e.Next() {
			handshake := e.Value.(TLSHandshakeDecoder.TLSHandshake)
			event := NewEvent(handshake.HandshakeType, clientSent)
			events.PushBack(event)
			log.Printf("Created Event:", event)
		}

	}
	return events
}

func DecomposeRecordLayer(tlsPayload []byte) list.List {
	if len(tlsPayload) < 5 {
		return list.List{}
	}
	log.Println("Parsing one packet......")
	var tlsLayerlist list.List
	total := uint16(len(tlsPayload))
	var offset uint16 = 0

	for offset < total {
		var p TLSHandshakeDecoder.TLSRecordLayer
		p.ContentType = uint8(tlsPayload[0+offset])
		p.Version = uint16(tlsPayload[1+offset])<<8 | uint16(tlsPayload[2+offset])
		p.Length = uint16(tlsPayload[3+offset])<<8 | uint16(tlsPayload[4+offset])
		p.Fragment = make([]byte, p.Length)
		l := copy(p.Fragment, tlsPayload[5+offset:5+p.Length+offset])
		tlsLayerlist.PushBack(p)
		log.Println("Length: ", p.Length, "Type: ", p.ContentType)
		offset += 5 + p.Length
		if l < int(p.Length) {
			fmt.Errorf("Payload to short: copied %d, expected %d.", l, p.Length)
		}
	}
	return tlsLayerlist
}

func DecomposeHandshakes(data []byte) list.List {
	if len(data) < 4 {
		return list.List{}
	}
	log.Println("Parsing one TLSLayer.......")
	var handshakelist list.List
	total := uint32(len(data))
	var offset uint32 = 0

	for offset < total {
		var p TLSHandshakeDecoder.TLSHandshake
		p.HandshakeType = uint8(data[0+offset])
		p.Length = uint32(data[1+offset])<<16 | uint32(data[2+offset])<<8 | uint32(data[3+offset])
		p.Body = make([]byte, p.Length)
		if p.Length < 2048 {
			l := copy(p.Body, data[4+offset:4+p.Length+offset])

			if l < int(p.Length) {
				fmt.Errorf("Payload to short: copied %d, expected %d.", l, p.Length)
			}
			offset += 4 + p.Length
		} else {
			p.HandshakeType = 99
			p.Length = 0
			offset = total
		}

		log.Printf("Handshake Type: %d, length: %d ", p.HandshakeType, p.Length)
		handshakelist.PushBack(p)
	}
	return handshakelist
}
