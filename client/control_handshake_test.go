package client

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/version"
)

func TestNewClientUsesSecureHandshakeWithNewServer(t *testing.T) {
	const verifyKey = "handshake-test-key"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer accepted.Close()
		serverResult <- runSecureHandshakePeer(accepted, verifyKey)
	}()

	clientConn, err := NewConnWithTLS("tcp", verifyKey, listener.Addr().String(), common.WORK_MAIN, "", false)
	if err != nil {
		t.Fatalf("new client failed to connect using secure handshake: %v", err)
	}
	_ = clientConn.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestNewClientFallsBackToLegacyServerHandshake(t *testing.T) {
	const verifyKey = "legacy-handshake-test-key"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverResult <- acceptErr
			return
		}
		defer accepted.Close()
		serverResult <- runLegacyHandshakePeer(accepted, verifyKey)
	}()

	clientConn, err := NewConnWithTLS("tcp", verifyKey, listener.Addr().String(), common.WORK_MAIN, "", false)
	if err != nil {
		t.Fatalf("new client failed to fall back to legacy handshake: %v", err)
	}
	_ = clientConn.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func readInitialHandshake(peer net.Conn) error {
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	c := conn.NewConn(peer)
	testFlag, err := c.GetShortContent(3)
	if err != nil {
		return err
	}
	if string(testFlag) != common.CONN_TEST {
		return fmt.Errorf("test flag = %q, want %q", testFlag, common.CONN_TEST)
	}
	coreVersion, err := c.GetShortLenContent()
	if err != nil {
		return err
	}
	if string(coreVersion) != version.GetVersion() {
		return fmt.Errorf("core version = %q, want %q", coreVersion, version.GetVersion())
	}
	productVersion, err := c.GetShortLenContent()
	if err != nil {
		return err
	}
	if string(productVersion) != version.VERSION {
		return fmt.Errorf("product version = %q, want %q", productVersion, version.VERSION)
	}
	return nil
}

func runSecureHandshakePeer(peer net.Conn, verifyKey string) error {
	if err := readInitialHandshake(peer); err != nil {
		return err
	}
	c := conn.NewConn(peer)
	if _, err := c.Write(crypt.SecureHandshakeMarker()); err != nil {
		return err
	}
	keyID, err := c.GetShortLenContent()
	if err != nil {
		return err
	}
	if got, want := string(keyID), string(crypt.SecureHandshakeKeyID(verifyKey)); got != want {
		return fmt.Errorf("key identifier = %x, want %x", keyID, []byte(want))
	}
	challenge, err := crypt.NewSecureHandshakeChallenge()
	if err != nil {
		return err
	}
	if err := c.WriteLenContent(challenge); err != nil {
		return err
	}
	payload, err := c.GetShortLenContent()
	if err != nil {
		return err
	}
	if err := crypt.VerifySecureHandshakeClientPayload(verifyKey, version.GetVersion(), version.VERSION, payload, challenge, time.Now().Unix(), 300); err != nil {
		return err
	}
	if _, err := c.Write([]byte(common.VERIFY_SUCCESS)); err != nil {
		return err
	}
	serverProof, err := crypt.SecureHandshakeServerProof(verifyKey, payload, challenge)
	if err != nil {
		return err
	}
	if _, err := c.Write(serverProof); err != nil {
		return err
	}
	flag, err := c.ReadFlag()
	if err != nil {
		return err
	}
	if flag != common.WORK_MAIN {
		return fmt.Errorf("connection type = %q, want %q", flag, common.WORK_MAIN)
	}
	return nil
}

func runLegacyHandshakePeer(peer net.Conn, verifyKey string) error {
	if err := readInitialHandshake(peer); err != nil {
		return err
	}
	c := conn.NewConn(peer)
	if _, err := c.Write([]byte(crypt.Md5(version.GetVersion()))); err != nil {
		return err
	}
	key, err := c.GetShortContent(32)
	if err != nil {
		return err
	}
	if string(key) != common.Getverifyval(verifyKey) {
		return errors.New("client did not fall back to the legacy VerifyKey digest")
	}
	if _, err := c.Write([]byte(common.VERIFY_SUCCESS)); err != nil {
		return err
	}
	flag, err := c.ReadFlag()
	if err != nil {
		return err
	}
	if flag != common.WORK_MAIN {
		return fmt.Errorf("connection type = %q, want %q", flag, common.WORK_MAIN)
	}
	return nil
}
