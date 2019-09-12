package models

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/asdine/storm"
	"github.com/gorilla/securecookie"
)

const SettingsID = 1

type Settings struct {
	ID           int64  `storm:"id,increment" json:"id"`
	SessionHash  []byte `json:"session_hash"`
	SessionBlock []byte `json:"session_block"`
	CsrfHash     []byte `json:"csrf_hash"`
	Host         string
	Scheme       string
}

func InitSettings(db *storm.DB) (Settings, error) {
	var s Settings
	db.One("ID", SettingsID, &s)
	if len(s.SessionHash) != 32 {
		s.SessionHash = securecookie.GenerateRandomKey(32)
	}

	if len(s.SessionBlock) != 16 {
		s.SessionBlock = securecookie.GenerateRandomKey(16)
	}

	if len(s.CsrfHash) != 32 {
		s.CsrfHash = securecookie.GenerateRandomKey(32)
	}

	if len(s.Host) == 0 {
		if os.Getenv("KUSHTAKA_ENV") == "development" {
			s.Host = "localhost:8080"
		} else {
			ip := GetOutboundIP().String()
			s.Host = fmt.Sprintf("%s:8080", ip)
		}
	}

	if s.Scheme != "http" || s.Scheme != "https" {
		s.Scheme = "http"
	}

	err := db.Save(&s)
	if err != nil {
		return s, err
	}

	return s, nil
}



func FindSettings(db *storm.DB) (*Settings, error) {
	var s Settings
	err := db.One("ID", SettingsID, &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Get preferred outbound ip of this machine
func GetOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP
}
