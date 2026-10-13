const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const root = path.resolve(__dirname, '..');
const shell = process.platform === 'win32' ? 'C:\\Program Files\\Git\\bin\\sh.exe' : 'sh';

// POSIX sh (dash on Debian/Ubuntu CI) rejects hyphenated function names such
// as apt-get(), so package managers are mocked as executables on PATH.
const stubDir = fs.mkdtempSync(path.join(os.tmpdir(), 'pm-install-test-'));
for (const name of ['apt-get', 'dnf', 'yum', 'zypper']) {
    fs.writeFileSync(path.join(stubDir, name), `#!/bin/sh\necho "${name} $*"\n`, { mode: 0o755 });
}
afterAll(() => fs.rmSync(stubDir, { recursive: true, force: true }));

function shellPath(p) {
    return process.platform === 'win32'
        ? p.replace(/^([A-Za-z]):/, (_, drive) => `/${drive.toLowerCase()}`).replace(/\\/g, '/')
        : p;
}

function install(manager, options = {}) {
    const result = spawnSync(shell, ['-c', `
set -eu
PATH="$STUB_BIN:$PATH"
export PATH
command() {
    if [ "$1" = "-v" ]; then
        case "$2" in
            apt-get|dnf|yum|zypper) [ "$2" = "$MOCK_PM" ] ;;
            curl|gpg) [ "$MISSING_TOOLS" = "false" ] ;;
            *) return 0 ;;
        esac
    else
        unset -f command
        command "$@"
    fi
}
id() { echo "$MOCK_UID"; }
uname() { echo "$MOCK_MACHINE"; }
dpkg() { echo "$MOCK_DEB_ARCH"; }
curl() { echo "curl $*"; }
gpg() {
    case "$1" in
        --show-keys) printf 'fpr:::::::::%s:\\n' "$MOCK_FINGERPRINT" ;;
        *) echo "gpg $*" ;;
    esac
}
rpm() { echo "rpm $*"; }
install() {
    echo "install $*"
    case "$*" in
        *printmaster.list|*printmaster.repo) cat "$3" ;;
    esac
}
rm() {
    case "$*" in
        *sources.list.d*) echo "rm $*" ;;
        *) command rm "$@" ;;
    esac
}
. "$INSTALLER"
`], {
        cwd: root,
        encoding: 'utf8',
        timeout: 10000,
        env: {
            ...process.env,
            INSTALLER: path.join(root, 'scripts', 'install-agent.sh'),
            STUB_BIN: shellPath(stubDir),
            MOCK_PM: manager,
            MOCK_UID: options.uid || '0',
            MOCK_MACHINE: options.machine || 'x86_64',
            MOCK_DEB_ARCH: options.debArch || 'amd64',
            MISSING_TOOLS: String(options.missingTools || false),
            MOCK_FINGERPRINT: options.fingerprint || '41CB14AF15B82312DE5ED2EAF5CF692407FFF7DF',
        },
    });
    if (result.error) throw result.error;
    return result;
}

test.each(['apt-get', 'dnf', 'yum', 'zypper'])('%s installs from the verified repository', manager => {
    const result = install(manager);
    expect(result.status).toBe(0);
    expect(result.stdout).toContain(`${manager} ${manager === 'zypper' ? '--non-interactive install' : 'install -y'} printmaster-agent`);
    if (manager === 'apt-get') {
        expect(result.stdout).toContain('signed-by=/etc/apt/keyrings/printmaster.gpg');
    } else {
        expect(result.stdout).toContain('rpm --import');
        expect(result.stdout).toContain('gpgcheck=1');
        expect(result.stdout).toContain(manager === 'zypper' ? '/etc/zypp/repos.d/printmaster.repo' : '/etc/yum.repos.d/printmaster.repo');
    }
});

test.each(['apt-get', 'dnf', 'yum', 'zypper'])('%s refuses a mismatched signing key before configuring the repo', manager => {
    const result = install(manager, { fingerprint: 'BAD' });
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain('fingerprint mismatch');
    expect(result.stdout).not.toContain('install -m');
    expect(result.stdout).not.toContain('rpm --import');
});

test.each([
    ['apt-get', { uid: '1000' }, 'as root'],
    ['pacman', {}, 'Unsupported distribution'],
    ['apt-get', { machine: 'armv7l' }, 'Unsupported architecture'],
    ['apt-get', { debArch: 'i386' }, 'Unsupported Debian architecture'],
])('rejects unsupported installation: %s %j', (manager, options, message) => {
    const result = install(manager, options);
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain(message);
    expect(result.stdout).not.toContain('curl ');
});

test.each([
    ['apt-get', 'gnupg'],
    ['dnf', 'gnupg2'],
    ['yum', 'gnupg2'],
    ['zypper', 'gpg2'],
])('%s installs missing prerequisites', (manager, gpgPackage) => {
    const result = install(manager, { missingTools: true });
    expect(result.status).toBe(0);
    expect(result.stdout).toContain(`ca-certificates curl ${gpgPackage}`);
});
