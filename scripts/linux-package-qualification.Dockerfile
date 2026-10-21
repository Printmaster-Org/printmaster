FROM fedora:44
RUN dnf install -y systemd rpm-build systemd-rpm-macros procps-ng && dnf clean all
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
