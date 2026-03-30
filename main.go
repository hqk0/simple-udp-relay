package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Address AddressConfig `toml:"ADDRESS"`
	Logs    LogsConfig    `toml:"LOGS"`
}

type AddressConfig struct {
	ListenAddress string `toml:"ListenAddress"`
	TargetAddress string `toml:"TargetAddress"`
}

type LogsConfig struct {
	Enable bool `toml:"Enable"`
}

func loadConfig(path string) (*Config, error) {
	cfg := Config{
		Address: AddressConfig{
			ListenAddress: "127.0.0.1:19134",
			TargetAddress: "127.0.0.1:19132",
		},
		Logs: LogsConfig{
			Enable: true,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("config.tomlが見つかりません。デフォルトの設定を使用します。")
			return &cfg, nil
		}
		return nil, fmt.Errorf("設定ファイルの読み込みに失敗しました: %w", err)
	}

	err = toml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, fmt.Errorf("TOMLのパースに失敗しました: %w", err)
	}

	return &cfg, nil
}

func main() {
	cfg, err := loadConfig("config.toml")
	if err != nil {
		log.Fatalf("エラー: %v", err)
	}

	if !cfg.Logs.Enable {
		log.SetOutput(io.Discard)
	} else {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	}

	log.Printf("UDPリレーの準備中... (Listen: %s -> Target: %s)\n", cfg.Address.ListenAddress, cfg.Address.TargetAddress)

	listenAddr, err := net.ResolveUDPAddr("udp", cfg.Address.ListenAddress)
	if err != nil {
		log.Fatalf("Listenアドレスの解析エラー: %v", err)
	}
	targetAddr, err := net.ResolveUDPAddr("udp", cfg.Address.TargetAddress)
	if err != nil {
		log.Fatalf("Targetアドレスの解析エラー: %v", err)
	}

	proxySock, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		log.Fatalf("UDPリッスンエラー: %v", err)
	}

	defer proxySock.Close()

	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		fmt.Printf("\nシグナルを受信しました: %v\n終了処理を開始します...\n", sig)
		proxySock.Close()
		done <- true
	}()

	clients := make(map[string]*net.UDPConn)
	var mu sync.Mutex

	go func() {
		buffer := make([]byte, 65535)

		for {
			n, clientAddr, err := proxySock.ReadFromUDP(buffer)
			if err != nil {
				break
			}

			clientKey := clientAddr.String()

			mu.Lock()
			targetSock, exists := clients[clientKey]

			if !exists {
				targetSock, err = net.DialUDP("udp", nil, targetAddr)
				if err != nil {
					log.Printf("ターゲットへの接続エラー (%s): %v", clientKey, err)
					mu.Unlock()
					continue
				}
				clients[clientKey] = targetSock
				log.Printf("新規クライアント接続: %s\n", clientKey)

				go func(cAddr *net.UDPAddr, tSock *net.UDPConn) {
					respBuffer := make([]byte, 65535)
					for {
						rn, err := tSock.Read(respBuffer)
						if err != nil {
							break
						}
						_, err = proxySock.WriteToUDP(respBuffer[:rn], cAddr)
						if err != nil {
							log.Printf("クライアントへの返信エラー (%s): %v", cAddr.String(), err)
						}
					}
				}(clientAddr, targetSock)
			}
			mu.Unlock()

			_, err = targetSock.Write(buffer[:n])
			if err != nil {
				log.Printf("ターゲットへの転送エラー (%s): %v", clientKey, err)
			}
		}
	}()

	log.Println("通信の待機を開始します... (Ctrl+C を押して終了)")

	<-done

	exitCode := 0
	fmt.Printf("プログラムを正常に終了しました。(終了コード: %d)\n", exitCode)
	os.Exit(exitCode)
}
