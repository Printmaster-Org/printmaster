FROM fedora:44
RUN dnf install -y systemd rpm-build systemd-rpm-macros procps-ng cpio && dnf clean all
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
