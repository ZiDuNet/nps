package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// en
func AesEncrypt(origData, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	origData = PKCS5Padding(origData, blockSize)
	blockMode := cipher.NewCBCEncrypter(block, key[:blockSize])
	crypted := make([]byte, len(origData))
	blockMode.CryptBlocks(crypted, origData)
	return crypted, nil
}

// de
func AesDecrypt(crypted, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	blockMode := cipher.NewCBCDecrypter(block, key[:blockSize])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	err, origData = PKCS5UnPadding(origData)
	return origData, err
}

// Completion when the length is insufficient
func PKCS5Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

// Remove excess
func PKCS5UnPadding(origData []byte) (error, []byte) {
	length := len(origData)
	unpadding := int(origData[length-1])
	if (length - unpadding) < 0 {
		return errors.New("len error"), nil
	}
	return nil, origData[:(length - unpadding)]
}

// Generate 32-bit MD5 strings
func Md5(s string) string {
	h := md5.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// Generating Random Verification Key
func GetRandomString(l int) string {
	if l <= 0 {
		return ""
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, l)
	max := big.NewInt(int64(len(alphabet)))
	for i := range result {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			// crypto/rand failures are exceptionally rare, but returning a
			// predictable secret would be worse than failing closed. Keep the
			// function's historical string-only API and make the failure visible.
			panic(fmt.Errorf("generate random secret: %w", err))
		}
		result[i] = alphabet[index.Int64()]
	}
	return string(result)
}

func GetVkey() string {
	// 生成UUID
	u, _ := uuid.NewRandom()
	// 将UUID转换为字符串
	uuidStr := u.String()
	uuidStr = strings.ReplaceAll(uuidStr, "-", "")
	// 截取前10位
	return uuidStr[:10]
}

func Base64Decoding(encodedString string) (string, error) {
	// 先尝试 base64 解码，兼容原先的 "nps " 前缀
	decodedBytes, err := base64.StdEncoding.DecodeString(encodedString)
	decodedString := string(decodedBytes)

	if err == nil {
		if len(decodedString) >= 4 && decodedString[:4] == "nps " {
			return decodedString[4:], nil
		}
	}
	// 兼容直接以 "nps:" 开头的旧格式：
	// nps:name|addr|key|tls
	if len(decodedString) >= 4 && strings.HasPrefix(decodedString, "nps:") {
		parts := strings.Split(decodedString[4:], "|")
		if len(parts) < 4 {
			return "", errors.New("快捷启动命令格式错误，请检查")
		}
		addr := strings.TrimSpace(parts[1])
		key := strings.TrimSpace(parts[2])
		tls := strings.TrimSpace(parts[3])
		// 返回兼容老服务端的格式："addr key tls"，不修改端口或 TLS 标志
		return addr + " " + key + " " + tls, nil
	}

	return "", errors.New("快捷启动命令错误，请检查")
}
