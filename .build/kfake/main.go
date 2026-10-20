// kfake 인메모리 Kafka 로 프로듀서/컨슈머 그룹 트래픽을 만드는 검증용 데모.
// 실행: .\.build\kfake.ps1  (콘솔은 별도로 --brokers 127.0.0.1:19092)
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// main 은 3 브로커 가짜 클러스터와 트래픽 생성기를 띄운다.
func main() {
	c, err := kfake.NewCluster(kfake.NumBrokers(3), kfake.Ports(19092, 19093, 19094),
		kfake.SeedTopics(6, "orders"), kfake.SeedTopics(3, "payments"), kfake.SeedTopics(1, "audit-log"), kfake.SeedTopics(2, "idle-topic"))
	if err != nil {
		log.Fatal(err)
	}
	seeds := c.ListenAddrs()
	log.Println("kfake up:", seeds)
	prod, err := kgo.NewClient(kgo.SeedBrokers(seeds...))
	if err != nil {
		log.Fatal(err)
	}
	produce := func(topic string, every time.Duration) {
		for i := 0; ; i++ {
			v := fmt.Sprintf(`{"id":%d,"topic":"%s","amount":%d,"ts":"%s"}`, i, topic, i*7%1000, time.Now().Format(time.RFC3339))
			prod.Produce(context.Background(), &kgo.Record{Topic: topic, Key: []byte(fmt.Sprint("k", i%20)), Value: []byte(v)}, nil)
			time.Sleep(every)
		}
	}
	go produce("orders", 20*time.Millisecond)
	go produce("payments", 100*time.Millisecond)
	go produce("audit-log", 500*time.Millisecond)

	consume := func(group string, delay time.Duration, topics ...string) {
		cl, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.ConsumerGroup(group), kgo.ConsumeTopics(topics...), kgo.AutoCommitInterval(time.Second))
		if err != nil {
			log.Fatal(err)
		}
		for {
			fs := cl.PollRecords(context.Background(), 10)
			fs.EachRecord(func(*kgo.Record) { time.Sleep(delay) })
		}
	}
	go consume("order-service", 0, "orders")
	go consume("billing", 30*time.Millisecond, "orders", "payments")
	go consume("audit-archiver", 0, "audit-log")
	select {}
}
