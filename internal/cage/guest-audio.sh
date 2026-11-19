# A virtual output shared by Cage applications. No hardware or microphone
# modules are loaded, and the PulseAudio protocol is Unix-socket only.
if ! command -v pulseaudio >/dev/null || ! command -v parec >/dev/null; then
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get -o DPkg::Lock::Timeout=60 update </dev/null
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get -o DPkg::Lock::Timeout=60 install -y --no-install-recommends pulseaudio pulseaudio-utils </dev/null
fi
# The PulseAudio package's own group for clients of a shared server.
getent group pulse-access >/dev/null || sudo groupadd --system pulse-access
sudo usermod -a -G pulse-access cage
sudo usermod -a -G pulse-access cage-browser
sudo install -d -m 0755 /etc/workbench /etc/pulse/client.conf.d
sudo tee /etc/pulse/client.conf.d/99-audio-output.conf >/dev/null <<'CLIENT'
default-server = unix:/run/audio-output/native
autospawn = no
CLIENT
sudo tee /etc/workbench/audio.pa >/dev/null <<'PULSE'
load-module module-null-sink sink_name=null rate=48000 channels=2 format=s16le
load-module module-native-protocol-unix socket=/run/audio-output/native auth-anonymous=0 auth-cookie-enabled=0 auth-group-enable=1 auth-group=pulse-access
set-default-sink null
set-default-source null.monitor
PULSE
sudo tee /etc/systemd/system/audio-output.service >/dev/null <<'UNIT'
[Unit]
Description=Virtual audio output
[Service]
User=cage
Group=pulse-access
Environment=HOME=/home/cage
Environment=PULSE_RUNTIME_PATH=/run/audio-output/runtime
Environment=PULSE_STATE_PATH=/run/audio-output/state
RuntimeDirectory=audio-output
RuntimeDirectoryMode=0750
ExecStart=/usr/local/libexec/service-log /usr/bin/pulseaudio -n --daemonize=no --exit-idle-time=-1 --disallow-exit --disallow-module-loading --disable-shm=true --realtime=no --high-priority=no --use-pid-file=no --file=/etc/workbench/audio.pa
LogNamespace=services
Restart=on-failure
RestartSec=1
TimeoutStopSec=5
NoNewPrivileges=true
UNIT
sudo chmod 0644 /etc/workbench/audio.pa /etc/pulse/client.conf.d/99-audio-output.conf /etc/systemd/system/audio-output.service
sudo systemctl daemon-reload
sudo systemctl reset-failed audio-output 2>/dev/null || true
sudo systemctl restart audio-output
for i in $(seq 1 50); do
  if pactl --server=unix:/run/audio-output/native info >/dev/null 2>&1; then break; fi
  sleep .1
done
pactl --server=unix:/run/audio-output/native info >/dev/null
