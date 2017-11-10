package main

import (
	"github.com/flix-tech/k8s-mdns/mdns"
	"log"
	"k8s.io/client-go/kubernetes"
	"k8s.io/api/core/v1"
	"fmt"
	"flag"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/apimachinery/pkg/watch"
	"net"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func mustPublish(rr string) {
	if err := mdns.Publish(rr); err != nil {
		log.Fatalf(`Unable to publish record "%s": %v`, rr, err)
	}
}

func mustUnPublish(rr string) {
	if err := mdns.UnPublish(rr); err != nil {
		log.Fatalf(`Unable to publish record "%s": %v`, rr, err)
	}
}

var (
	master = flag.String("master", "", "url to master")
	test = flag.Bool("test", false, "testing mode, no connection to k8s")
)

func main() {
	flag.Parse()

	if *test {
		mustPublish("router.local. 60 IN A 192.168.1.254")
		mustPublish("254.1.168.192.in-addr.arpa. 60 IN PTR router.local.")

		select{ }
	}

	// uses the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags(*master, "")
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	for {
		services, err := clientset.CoreV1().Services("").Watch(metaV1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		for {
			ev := <-services.ResultChan()

			if ev.Object == nil {
				log.Fatalln("Error during watching")
			}

			service := ev.Object.(*v1.Service)
			ip := net.ParseIP(service.Spec.ClusterIP)
			if ip == nil {
				continue
			}

			reverseIp := net.IPv4(ip[15], ip[14], ip[13], ip[12])

			record := fmt.Sprintf("%s.%s.local. 60 IN A %s", service.Name, service.Namespace, ip)
			reverseRecord := fmt.Sprintf("%s.in-addr.arpa. 60 IN PTR %s.%s.local.", reverseIp, service.Name, service.Namespace)
			switch ev.Type {
			case watch.Added:
				log.Printf("Added dns record %s.%s.local. with %s\n", service.Name, service.Namespace, ip)
				mustPublish(record)
				mustPublish(reverseRecord)
			case watch.Deleted:
				log.Printf("Removed dns record %s.%s.local. with %s\n", service.Name, service.Namespace, ip)
				mustUnPublish(record)
				mustUnPublish(reverseRecord)
			case watch.Modified:
				// ignore
			}
		}

	}
}
