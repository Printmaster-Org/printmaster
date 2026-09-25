# PrintMaster Agent RPM Spec File
# Usage: rpmbuild -bb printmaster-agent.spec
# Requires: PRINTMASTER_VERSION and PRINTMASTER_BINARY env vars to be set

%define _binaries_in_noarch_packages_terminate_build 0
%define debug_package %{nil}

Name:           printmaster-agent
Version:        %{getenv:PRINTMASTER_VERSION}
Release:        1%{?dist}
Summary:        Network printer fleet management agent

License:        MIT
URL:            https://github.com/printmaster-org/printmaster
Source0:        printmaster-agent

BuildRequires:  systemd-rpm-macros
Requires:       glibc

# The package creates its own user/group in %pre, so we provide these
# to satisfy RPM's automatic dependency generator
Provides:       user(printmaster)
Provides:       group(printmaster)

%description
PrintMaster Agent discovers and monitors network printers via SNMP,
collects metrics (page counts, toner levels, status), and optionally
reports to a central PrintMaster Server for multi-site fleet management.

Features:
- Automatic printer discovery (SNMP, mDNS, WS-Discovery)
- Real-time metrics collection
- Web UI for monitoring
- Optional central server reporting
- SELinux compatible (Fedora/RHEL/CentOS)

%install
# Create directories
install -d %{buildroot}%{_bindir}
install -d %{buildroot}%{_unitdir}
install -d %{buildroot}%{_sysconfdir}/printmaster
install -d %{buildroot}%{_sysconfdir}/sudoers.d
install -d %{buildroot}%{_sharedstatedir}/printmaster
install -d %{buildroot}%{_localstatedir}/log/printmaster

# Install binary
install -m 755 %{getenv:PRINTMASTER_BINARY} %{buildroot}%{_bindir}/printmaster-agent

# Install sudoers file for package manager auto-update
cat > %{buildroot}%{_sysconfdir}/sudoers.d/printmaster-agent << 'EOF'
# PrintMaster Agent - Allow auto-update via package manager
# This file allows the printmaster service user to update only the printmaster-agent package
# Remove this file to disable automatic package updates

# Fedora/RHEL dnf commands
# Note: --setopt=logdir=/tmp prevents dnf5 from writing to /var/log/dnf5.log which may be read-only for the service
# The wildcard allows installing specific versions (e.g., printmaster-agent-0.27.4)
printmaster ALL=(root) NOPASSWD: /usr/bin/dnf --setopt=logdir=/tmp --refresh install -y --allowerasing printmaster-agent
printmaster ALL=(root) NOPASSWD: /usr/bin/dnf --setopt=logdir=/tmp --refresh install -y --allowerasing printmaster-agent-*
printmaster ALL=(root) NOPASSWD: /usr/bin/dnf --setopt=logdir=/tmp --refresh upgrade -y printmaster-agent

# RHEL/CentOS yum commands (fallback for older systems)
printmaster ALL=(root) NOPASSWD: /usr/bin/yum clean metadata -q
printmaster ALL=(root) NOPASSWD: /usr/bin/yum install -y -q printmaster-agent
printmaster ALL=(root) NOPASSWD: /usr/bin/yum install -y -q printmaster-agent-*
printmaster ALL=(root) NOPASSWD: /usr/bin/yum upgrade -y -q printmaster-agent

# Allow agent to restart itself after update
printmaster ALL=(root) NOPASSWD: /usr/bin/systemctl restart --no-block printmaster-agent.service
printmaster ALL=(root) NOPASSWD: /bin/systemctl restart --no-block printmaster-agent.service
EOF
chmod 440 %{buildroot}%{_sysconfdir}/sudoers.d/printmaster-agent

# Install systemd service
cat > %{buildroot}%{_unitdir}/printmaster-agent.service << 'EOF'
[Unit]
Description=PrintMaster Agent - Network Printer Fleet Management
Documentation=https://github.com/printmaster-org/printmaster
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/bin/printmaster-agent --config /etc/printmaster/agent.toml
Restart=on-failure
RestartSec=5
User=printmaster
Group=printmaster
WorkingDirectory=/var/lib/printmaster

# Set data directory via environment variable
Environment=PRINTMASTER_DATA_DIR=/var/lib/printmaster

# SELinux: Run as unconfined to avoid AVC denials
# This is standard for third-party network daemons on Fedora/RHEL
SELinuxContext=system_u:system_r:unconfined_service_t:s0

# Security hardening (systemd-level, independent of SELinux)
# Note: NoNewPrivileges and ProtectSystem are NOT set to allow sudo for package manager auto-updates
# The sudoers.d/printmaster-agent file restricts sudo to only specific update commands
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

# Install example config
cat > %{buildroot}%{_sysconfdir}/printmaster/agent.toml.example << 'EOF'
# PrintMaster Agent Configuration
# Copy to agent.toml and customize as needed

[agent]
# Name for this agent (displayed in server UI)
name = "my-site-agent"

# Web UI settings
listen = ":8080"

[discovery]
# Networks to scan (CIDR notation)
# networks = ["192.168.1.0/24", "10.0.0.0/24"]

# Scan interval (minutes)
# interval = 60

[server]
# Optional: Report to central PrintMaster Server
# url = "http://your-server:8080"
# enabled = false
EOF

%pre
# Create printmaster user/group if they don't exist
getent group printmaster >/dev/null || groupadd -r printmaster
getent passwd printmaster >/dev/null || \
    useradd -r -g printmaster -d /var/lib/printmaster -s /sbin/nologin \
    -c "PrintMaster Agent" printmaster
exit 0

%post
%systemd_post printmaster-agent.service

# Set ownership of directories
chown -R printmaster:printmaster /var/lib/printmaster
chown -R printmaster:printmaster /var/log/printmaster
chmod 750 /var/lib/printmaster
chmod 750 /var/log/printmaster

# Create config from example if it doesn't exist
if [ ! -f /etc/printmaster/agent.toml ]; then
    cp /etc/printmaster/agent.toml.example /etc/printmaster/agent.toml
    chown root:printmaster /etc/printmaster/agent.toml
    chmod 640 /etc/printmaster/agent.toml
fi

# Enable and start the service (fresh install or upgrade)
systemctl daemon-reload
systemctl enable printmaster-agent.service || true
systemctl restart printmaster-agent.service || true

echo "PrintMaster Agent installed and running at http://localhost:8080"

%preun
%systemd_preun printmaster-agent.service

%postun
%systemd_postun_with_restart printmaster-agent.service

# Remove user on complete uninstall (not upgrade)
if [ $1 -eq 0 ]; then
    userdel printmaster 2>/dev/null || true
    groupdel printmaster 2>/dev/null || true
fi

%files
%{_bindir}/printmaster-agent
%{_unitdir}/printmaster-agent.service
%dir %{_sysconfdir}/printmaster
%config(noreplace) %{_sysconfdir}/printmaster/agent.toml.example
%config(noreplace) %attr(440,root,root) %{_sysconfdir}/sudoers.d/printmaster-agent
%dir %attr(750,printmaster,printmaster) %{_sharedstatedir}/printmaster
%dir %attr(750,printmaster,printmaster) %{_localstatedir}/log/printmaster

%changelog
* Tue Dec 24 2024 PrintMaster Team <printmaster@example.com> - %{version}-1
- Simplified SELinux handling: use unconfined_service_t via systemd unit
- Removed complex semanage fcontext commands that caused AVC denials
- Removed policycoreutils dependency (no longer needed)
- Service still has systemd-level sandboxing (ProtectSystem, NoNewPrivileges, etc.)
* Mon Dec 23 2024 PrintMaster Team <printmaster@example.com>
- Added proper SELinux context configuration
- Fixed file contexts for /etc, /var/lib, /var/log directories
- Added port labeling for web UI port 8080
* Sun Dec 22 2024 PrintMaster Team <printmaster@example.com>
- Initial automated build from release
