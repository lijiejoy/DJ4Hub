#!/bin/sh
set -eu

runtime=/tmp/dj4hub-voice
route_pid=/run/dj4hub-voice-route.pid
route_log=/run/dj4hub-voice-route.log
calibration_pid=/run/dj4hub-voice-calibration.pid
calibration_log=/run/dj4hub-voice-calibration.log
helper=$runtime/mavo-pcm-bridge.armv7

# Check executable identity and start time before trusting a PID file.
owned() {
    test -s "$1" || return 1
    read pid expected_start < "$1" || return 1
    case "$pid:$expected_start" in
        :*|*:|*[!0-9:]*) return 1 ;;
    esac
    test -r "/proc/$pid/stat" || return 1
    test "$(cut -d ' ' -f 22 "/proc/$pid/stat")" = "$expected_start" || return 1
    test "$(tr '\000' '\n' < "/proc/$pid/cmdline" | sed -n '1p')" = "$2"
}

ready() {
    owned "$calibration_pid" /usr/bin/alsaucm_test &&
        grep -q 'ACDB -> Sent VocProc Cal!' "$calibration_log" &&
        owned "$route_pid" "$helper" &&
        grep -q 'VoLTE route session active on hw:0,4' "$route_log" &&
        test "$(cat /sys/class/android_usb/f_audio/audio_enable)" = 1 &&
        grep -q '^state: RUNNING' /proc/asound/card0/pcm4p/sub0/status &&
        grep -q '^state: RUNNING' /proc/asound/card0/pcm4c/sub0/status
}

rotate_route() {
    if owned "$route_pid" "$helper"; then
        kill -TERM "$pid"
        n=0
        while owned "$route_pid" "$helper" && test "$n" -lt 50; do
            sleep 0.1
            n=$((n+1))
        done
        if owned "$route_pid" "$helper"; then
            echo "Previous DJ4Hub voice route did not stop."
            return 1
        fi
    fi
    if pidof mavo-pcm-bridge.armv7 >/dev/null; then
        echo "Another application owns a voice route."
        return 1
    fi
    nohup "$helper" --voice-route-session --verbose </dev/null > "$route_log" 2>&1 &
    pid=$!
    start=$(cut -d ' ' -f 22 "/proc/$pid/stat")
    printf '%s %s\n' "$pid" "$start" > "$route_pid"
    n=0
    while test "$n" -lt 50; do
        if ready; then
            echo "DJ4HUB_VOICE_ROUTE_READY"
            return 0
        fi
        sleep 0.1
        n=$((n+1))
    done
    tail -n 60 "$route_log"
    return 1
}

test "$(id -u)" = 0
test "$(uname -r)" = 3.18.44
test "$(uname -m)" = armv7l
test "$(cat /sys/class/android_usb/android0/idVendor)" = 2ca3
test "$(cat /sys/class/android_usb/android0/idProduct)" = 4006

verify() {
    actual=$(sha256sum "$runtime/$1")
    test "${actual%% *}" = "$2"
}
verify qdc507_aprv3.ko 3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a
verify qdc507_voice.ko ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c
verify mavo-pcm-bridge.armv7 88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc
if test "${1:-}" = --check-ready; then
    ready
    echo "DJ4HUB_VOICE_ROUTE_READY"
    exit 0
fi
if test "${1:-}" = --rotate-ready; then
    ready
    rotate_route
    exit 0
fi
chmod 755 "$helper"

if grep -q '^qdc507_afe ' /proc/modules; then
    echo "An incompatible legacy audio driver is already loaded."
    exit 1
fi
for module in qdc507_aprv3 qdc507_voice; do
    if ! grep -q "^$module " /proc/modules; then
        insmod "$runtime/$module.ko"
    fi
done
grep -Fq mdm9607-tomtom-i2s-snd-card /proc/asound/cards
for device in controlC0 pcmC0D4p pcmC0D4c pcmC0D5p pcmC0D6c; do
    test -c "/dev/snd/$device"
done

if ! owned "$calibration_pid" /usr/bin/alsaucm_test; then
    # Do not disturb a calibration service started by another application.
    if pidof alsaucm_test >/dev/null; then
        echo "Another application owns the calibration service."
        exit 1
    fi
    test ! -e /run/alsaucm_test
    nohup /usr/bin/alsaucm_test </dev/null > "$calibration_log" 2>&1 &
    pid=$!
    start=$(cut -d ' ' -f 22 "/proc/$pid/stat")
    printf '%s %s\n' "$pid" "$start" > "$calibration_pid"
    n=0
    while test ! -p /run/alsaucm_test && test "$n" -lt 50; do
        kill -0 "$pid"
        sleep 0.1
        n=$((n+1))
    done
    test -p /run/alsaucm_test
fi

write_calibration() {
    timeout -t 3 -s KILL sh -c 'printf "%s\n" "$1" > /run/alsaucm_test' sh "$1"
}

if ! grep -q 'ACDB -> Sent VocProc Cal!' "$calibration_log"; then
    write_calibration 'open snd_soc_msm_9x07_Tomtom_I2S'
    write_calibration 'set _verb VoLTE'
    write_calibration 'set _enadev Auxpcm Rx'
    write_calibration 'set _enadev Auxpcm Tx'
    n=0
    while ! grep -q 'ACDB -> Sent VocProc Cal!' "$calibration_log" && test "$n" -lt 100; do
        sleep 0.1
        n=$((n+1))
    done
    if ! grep -q 'ACDB -> Sent VocProc Cal!' "$calibration_log"; then
        tail -n 60 "$calibration_log"
        exit 1
    fi
fi

test -c /dev/ttyGS0
test -p /run/voc_svr
"$helper" --check
if test "${1:-}" != --rotate && owned "$route_pid" "$helper"; then
    echo "Existing DJ4Hub voice route is still active."
else
    rotate_route
    exit 0
fi

n=0
while test "$n" -lt 50; do
    if ready; then
        echo "DJ4HUB_VOICE_ROUTE_READY"
        exit 0
    fi
    sleep 0.1
    n=$((n+1))
done
tail -n 60 "$route_log"
exit 1
