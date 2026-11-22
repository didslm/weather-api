#!/bin/sh
# Provision stable, root-owned infrastructure. Run manually, never from a release.
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo' >&2; exit 1; }
phone_files=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
for dependency in python3 nginx git curl systemctl go runuser; do
    command -v "$dependency" >/dev/null || { echo "Missing $dependency" >&2; exit 1; }
done
install -d -m 755 /usr/local/sbin /opt/weather-risk-api /opt/weather-risk-api/releases
install -d -o ayu -g ayu -m 700 /var/lib/weather-risk-api
install -d -o ayu -g ayu -m 755 /var/cache/weather-risk-api
install -d -o ayu -g ayu -m 755 /home/ayu/.local/share/applications
install -d -o ayu -g ayu -m 755 /home/ayu/.local/share/icons/hicolor/scalable/apps
install -m 755 "$phone_files/weather_deploy.py" /usr/local/sbin/weather-deploy
install -m 755 "$phone_files/weather_dashboard.py" /usr/local/sbin/weather-dashboard.py
install -d -m 755 /usr/local/share/phone-services
install -m 644 "$phone_files/phone_services.html" /usr/local/share/phone-services/index.html
install -o ayu -g ayu -m 644 "$phone_files/weather-dashboard.desktop" /home/ayu/.local/share/applications/weather-dashboard.desktop
install -o ayu -g ayu -m 644 "$phone_files/weather-dashboard.svg" /home/ayu/.local/share/icons/hicolor/scalable/apps/weather-dashboard.svg
install -m 644 "$phone_files/nginx.conf" /opt/weather-risk-api/nginx.conf
for service in weather-release@.service weather-proxy.service weather-update.service weather-update.timer weather-dashboard.service; do
    install -m 644 "$phone_files/$service" "/etc/systemd/system/$service"
done
if [ ! -f /etc/weather-deploy.json ]; then
    printf '%s\n' '{"public_url":"","soak_seconds":20}' > /etc/weather-deploy.json
    chmod 644 /etc/weather-deploy.json
fi
systemctl daemon-reload
systemctl enable --now weather-dashboard.service
echo 'Infrastructure installed. Local status dashboard: http://127.0.0.1:8084'
