FROM scratch
ADD k8s-mdns /
ENTRYPOINT ["/k8s-mdns", "--logtostderr"]
