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
	default_namespace = flag.String("default-namespace", "default", "namespace in which services should also be published with a shorter entry")
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

			records := []string{
				fmt.Sprintf("%s.%s.local. 120 IN A %s", service.Name, service.Namespace, ip),
				fmt.Sprintf("%s.in-addr.arpa. 120 IN PTR %s.%s.local.", reverseIp, service.Name, service.Namespace),
			}

			if service.Namespace == *default_namespace {
				records = append(records, fmt.Sprintf("%s.local. 120 IN A %s", service.Name, ip))
			}

			switch ev.Type {
			case watch.Added:
				for _, record := range records {
					log.Printf("Added %s\n", record)
					mustPublish(record)
				}
			case watch.Deleted:
				for _, record := range records {
					log.Printf("Remove %s\n", record)
					mustUnPublish(record)
				}
			case watch.Modified:
				// ignore
			}
		}

	}
}
