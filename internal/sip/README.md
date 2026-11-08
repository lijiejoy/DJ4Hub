# SIP voice bridge (Item 1)

Local SIP server + RTP media bridge for DJ4Hub on macOS (AT mode,
loopback-first). Follows xiaoguiday/vohive's config shape
(`sip.listen` UDP 5060, realm `vohive.local`, RTP 10000–20000, one local
user, default `linphone`) and CellBridge's verified media shape (SIP 5060
+ RTP PCMU/8000). The QDC507 module runtime uses a verified, temporary
ADB preparation step described below. No fixed SIP secrets are used.

## Software capabilities

- UDP SIP server on `127.0.0.1:5060`: REGISTER + INVITE + ACK + BYE +
  CANCEL + OPTIONS with MD5 Digest auth (RFC 2617, `qop=auth`).
- Outbound INVITE → `ATD` dial; inbound RING/CLIP → INVITE to registered
  client(s); ATH on BYE/CANCEL; call state via `GET /api/sip/status`.
- RTP PCMU/8000 sessions in 10000–20000, G.711 u-law encode/decode,
  silence frames + packet accounting while the call is active.
- HTTP API: `GET /api/sip/status`, `POST /api/sip/call`,
  `POST /api/sip/hangup`, `POST /api/sip/answer`,
  `POST /api/sip/panel`, `GET/POST /api/sip/settings`; Calls tab in the web UI.
- Native AppKit call panel: menu-bar dialpad, persistent incoming/active-call
  window, microphone/speaker mute and per-call modem DTMF (digits, `*`, `#`).
  Window actions carry exact call IDs over private pipes; closing a window
  leaves the service and current call running. Call and media state are owned
  by the service, with no duplicate call-control state in the menu-bar client.
- First-run password: generated on first run, stored mode `0600` in
  `~/Library/Application Support/DJ 4G Hub/sip-password`. Its value is
  never logged, hardcoded or returned by the API.

## Cellular call verification

- Outbound dial, connection and automatic hangup were verified on the
  connected QDC507 on 2026-11-07. After restoring the module runtime,
  the tester heard the SIP test tone and the return path contained
  nonzero audio. A subsequent local Mac call also transferred nonzero
  audio in both directions. The tester confirmed hearing both sides, then
  reported strong acoustic echo and noticeable delay.
- Real incoming detection has produced call records with caller ID on the
  attached USB-AT module. The updated native window receives later caller-ID
  snapshots; live incoming-answer acceptance with that window is still needed.
- Inbound RING/CLIP → SIP INVITE is wired; external SIP-client inbound media
  still needs its own live test.
- The legacy `AT+QPCMV` bridge is not used for production PCM. Manual
  enable attempts returned `ERROR`, including during a connected call.

## CoreAudio media bridge

The macOS service uses `NewCallAudioBridge`, built on `NewUSBAudioBridge`,
to connect module capture and playback to PCMU RTP or local Mac audio.
It matches module CoreAudio device UIDs
against IOKit USB engines with VID/PID `2ca3:4006`; it never selects the
Mac's default microphone or speakers. Both directions must expose
8 kHz mono linear PCM (signed 16-bit or float32 in the CoreAudio client
format). Multiple matching modules fail with an explicit error.

CoreAudio callbacks run in C, using bounded single-producer/
single-consumer buffers without allocating, taking locks or calling Go.
Streams start when RTP starts, and stop on hangup, stream failure or
device removal. Late startup results are released after cancellation.
Status exposes device identities, stream state, captured/played sample
counts, nonzero sample counts, overflows, underruns and the last error.
Media failures end the SIP call instead of presenting a silent success.

The implementation requires macOS microphone permission for module
capture. The binary embeds a usage description and checks authorization
before creating callbacks. Permission requests have a timeout and an
actionable error. Enabled call-service startup requests the initial grant
without opening microphone streams. A shared gate prompts at most once per
process, while checking OS authorization on every audio open so revocation
and grants in Settings take effect. The OS retains permission; replacing an
unsigned build may require a new grant. Launching from Terminal may attribute permission to
Terminal; background launchd execution may require foreground startup
to grant access. Audio startup is bounded if CoreAudio stalls.

The old `QPCMVBridge` remains for demo mode and protocol unit tests. Its
PCM methods are stubs; it is not the production macOS media path.

Web/API calls prepare the module route and initialize a stopped Mac audio
unit during dialing/ringing. Mac microphone and speaker callbacks start only
after the modem reports connection or the user answers; declining or remote
hangup releases prepared devices without starting the microphone. Physical
validation confirmed zero capture before answer and callback startup in
about 188 ms after starting the prepared unit (not total cellular delay).
The service now warms only the module-side route in the background after SIP
startup. Its private ADB server is reused until bridge shutdown. Each call
still verifies USB identity, runtime hashes, owned process identity, calibration
and running PCM state. Every media session renews its owned DSP route,
retaining verified resources and calibration; unhealthy routes use full recovery.
Reusing a running route across cellular calls caused silent subsequent calls.
On the Intel test machine, preparation fell from 7.359 s to 2.192–2.221 s
after background warmup and per-call route renewal. The stopped Mac unit started callbacks in about
193 ms; these measurements are not cellular answer-to-speech latency.
A single VoiceProcessingIO audio unit renders
the far-end signal and captures echo-cancelled microphone audio, converting
to/from 8 kHz signed mono PCM. Processing bypass is explicitly disabled and
read back before startup; initialization fails instead of silently falling
back to an unprocessed microphone. Callbacks and bounded buffers remain in C.
The worker drains actual device batches at 5 ms intervals, up to 40 ms per
batch, with no initial 80 ms output queue. It does not synthesize silent
microphone frames between asynchronous hardware callbacks. Playback backlog
limits account for producer batch size as well as consumer callback size,
so a normal device burst is not mistaken for excessive latency.
Consumers discard stale audio beyond one hardware/worker frame plus 20 ms of
headroom after scheduling stalls or clock drift. This bounds application
backlog, not total cellular or hardware latency.
The module reported a 512-frame (64 ms) hardware buffer in each direction.
During a session the bridge requests 128 frames (16 ms) if supported, and
restores the previous setting on cleanup if it still matches our request.
Unsupported devices retain their existing hardware buffer.

Live modem reconciliation now runs once per second. USB-AT incoming-call
detection has its own one-second poll instead of waiting for an eight-second
SMS scan. Web call status also refreshes globally once per second, regardless
of the selected page. Incoming calls show a modal with caller ID, answer,
decline and ringer mute; answer/hangup sends the exact displayed call ID.
Caller cancellation closes the modal and stops the ringer. Browser sound
needs an initial user gesture; "开启来电提醒" also requests browser notification
permission for alerts while the page is in the background. The page must
remain open. Browser/OS notification permissions and background timer
throttling still affect background alerts.
The macOS cgo build also provides an independent native incoming-call window
and ringer, without requiring the browser to stay open. The window updates
pending caller ID in place and has separate red decline and green answer
buttons. USB-AT monitoring enables `AT+CLIP=1` and continues caller-ID polling
while ringing. Native and browser ringers are currently independent; browser
mute does not mute the native ringer. See [the feature audit](../../docs/2026-11-07-feature-audit.md)
for verification boundaries and unresolved issues.
The local worker stops on hangup or device failure, including cleanup
of a startup that finishes after cancellation. SIP-client calls continue
to own their RTP audio and never open the Mac microphone or speakers.

Optional `~/Library/Application Support/DJ 4G Hub/call-audio.json` selects
local endpoints by exact device name or UID:

```json
{"enabled":true,"input_device":"","output_device":""}
```

Empty strings select built-in Mac devices, deliberately avoiding a virtual
default microphone. Local input/output identities, sample counts, state
and errors appear in `audio.local`, along with `echo_cancellation`, queue
depths in samples, discarded samples and underruns. At 8 kHz, 80 samples
represent 10 ms. Module queue depths are exposed in `audio` as well.
Forwarded sample counts, current signal peaks and cumulative clipped samples
are exposed for both speech directions without recording speech.
Existing `audio.detail` describes
local startup, operation or failure. No speech is recorded or saved.

### Validation tools

Build the diagnostic tool:

```sh
go build -o dist/dj4hub-call-test ./cmd/dj4hub-call-test
```

Run without arguments to check the physical module audio callbacks for
two seconds. It sends silence and never places a call:

```sh
./dist/dj4hub-call-test
```

Read formats and master/mono mute and volume controls without opening
streams, requesting microphone access or dialing:

```sh
./dist/dj4hub-call-test -inspect-audio
```

Provision the pinned module runtime in the private user cache and
prepare the module-side route without dialing:

```sh
./dist/dj4hub-call-test -prepare-module
```

Check the Mac microphone/speaker callbacks with silent playback and
without saving captured audio or dialing:

```sh
./dist/dj4hub-call-test -check-local-audio
```

The output includes property-read OSStatus values. A missing mute or
volume field means the property could not be read, not that the device
is unmuted or its volume is zero. Inspection cannot be combined with
`-number`.

An explicit number enables an authenticated SIP test against the
running local service. This places a real call, sends a quiet 440 Hz
intermittent tone for 15 seconds after answering, checks return audio,
and hangs up. Use only an authorized test number:

```sh
./dist/dj4hub-call-test -number YOUR_TEST_NUMBER -duration 15s
```

The tool reads existing local SIP credentials without printing them.
It saves no audio. Its loopback integration test uses fake call-control
and PCM endpoints, never a real modem.

Normal tests never open audio devices. Physical callback verification
is explicitly opt-in:

```sh
DJ4HUB_TEST_USB_AUDIO=1 go test ./internal/sip -run '^TestNativeModuleAudio$' -v -count=1 -timeout=30s
```

## Initial Hardware Diagnosis (2026-11-07)

This section records the initial failed tests. The subsequent recovery
and successful signal-transfer checks are recorded below.

Read-only checks on a connected Baiwang QDC507, firmware QDC507GLEFM21,
confirmed:

- USB device `2ca3:4006` exposes audio control interface 6 and audio
  streaming interfaces 7 and 8. macOS attaches AppleUSBAudioDevice.
- CoreAudio lists BAIWANG `AC Interface` as an 8 kHz mono input and
  `AS Interface` as an 8 kHz mono output.
- `AT+QPCMV=?` returns `+QPCMV: (0,1),(0-2)` and `OK`.
- `AT+QPCMV?` returns `ERROR`. The rejected read syntax does not imply
  that the command or USB audio is unsupported.
- `AT+QCFG="usbcfg"` returns
  `+QCFG: "usbcfg",0x2CA3,0x4006,1,1,1,1,1,1,1`.

The running service reported no attached modem in its SIP audio status
while its health API showed a working USB AT connection. The audio
adapter used to be discarded when no modem was present at SIP startup.
It now remains attached to the app and checks connection availability
on each status request, including after USB discovery or disconnection.
The legacy adapter still supports this dynamic attachment check.
The production bridge now reports actual matching audio device availability.

The initial media blocker was the unimplemented CoreAudio PCM bridge.
That bridge is now implemented; the current blocker is silent capture
from the module during connected cellular calls. The RTP loopback test
`TestRTPTransfersNonSilentPCMInBothDirections` verifies non-silent PCM
encoding, UDP transport, decoding and delivery to an injected endpoint.
It does not verify physical module audio or a cellular call.

Physical callbacks were verified after foreground startup from Terminal
with microphone permission authorized: a two-second no-call check
captured 15,872 samples and played 16,384 samples, with zero dropped
samples. The launchd process still reported permission `not_determined`
in this environment, so the live tests used the foreground service.
Foreground authorization does not establish background-service access.

Three authorized outbound cellular tests connected and ended through
SIP BYE. The diagnostic sent a quiet intermittent tone without saving
audio. Results:

| Test | RTP packets sent | RTP packets received | Decoded return samples | Return samples above threshold | Peak |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 1,000 | 495 | 79,200 | 0 | 0 |
| 2 | 1,250 | 489 | 78,240 | 0 | 0 |
| 3 | 1,000 | 489 | 78,240 | 0 | 0 |

The final service counters showed 78,336 captured samples, all zero,
and 78,848 played samples, including 39,600 nonzero samples, with zero
drops and 512 startup underrun samples. Playback counters show delivery
to CoreAudio; they do not prove delivery to the cellular uplink. The
tester subsequently confirmed speaking into the phone and hearing no
test tone. Both intended audio directions therefore failed this live
acceptance check.

Read-only CoreAudio inspection subsequently confirmed both endpoints
use float32 client PCM (format flags 9), 8,000 Hz, one channel and four
bytes per frame, which the bridge supports. Master and channel-one mute
and volume reads all returned OSStatus 2003332927 (unsupported property).
There is no readable CoreAudio mute/gain setting on these endpoints
that explains the observed silence.

`AT+QPCMV=1,2` returned `ERROR` both idle and connected. Idle attempts
with `=1` and `=1,0` also failed. The alternate AT control interface
returned the same capability and query results. These observations do
not establish whether the cause is module voice routing, firmware
behavior, or another module-side prerequisite. No firmware was flashed,
USB configuration changed, or module rebooted during these tests.

At this stage the SIP/CoreAudio transport implementation could not be
described as an accepted two-way voice feature.

### Module-side route investigation

The reference implementation in
[`CellBridge`'s QDC507 adapter](https://github.com/mccding/CellBridge/blob/main/gateway/internal/voice/qdc507/audio.go)
does more than open host USB audio: it prepares module-side calibration
and a voice-route runtime before starting the host streams. Its
[`README`](https://github.com/mccding/CellBridge/blob/main/README.md)
states that the module runtime binaries are not distributed with the
repository. Its documented hardware identity also differs from this
device; compatibility with QDC507GLEFM21 has not been established.
This is evidence for investigating a missing module-side route, not
proof that installing that runtime will fix this device.

Local USB inspection found an ADB-class interface 5 (class/subclass/
protocol `ff/42/01`). ADB 36.0.0 using its libusb backend listed no
attached devices, but an isolated server started with `ADB_LIBUSB=0`
on port 5041 discovered this module. Existing debug access was already
available as UID 0; no access-unlock operation was necessary. The USB
location matched the CoreAudio module location, and device-side reads
confirmed VID/PID `2ca3:4006`.

Read-only module inspection then established:

- Kernel release: `3.18.44`.
- `/proc/asound/cards`: `--- no soundcards ---`.
- `/dev/snd`: only `timer`; no PCM capture or playback devices.
- `/sys/class/android_usb/f_audio/audio_enable`: `0`.
- `/proc/modules`: empty.
- `/proc/config.gz`: `CONFIG_SND_SOC_MDM9607`,
  `CONFIG_SND_SOC_QDSP6V2`, `CONFIG_MSM_QDSP6_APRV2` and
  `CONFIG_MSM_QDSP6_APRV3` are not set.
- The installed `/usr/lib/modules/3.18.44` tree contains no corresponding
  voice/APR modules; `/data/acdb` is empty.
- `alsaucm_test` and `qti_audio_server_daemon` exist, but no running
  module-side audio route was observed.

This establishes a concrete module-side prerequisite failure for the
reference ALSA/VoLTE route: there is no internal soundcard/PCM endpoint
to connect to the USB gadget. Host-side CoreAudio callbacks alone cannot
establish that route. A compatible external driver/runtime may supply
the missing pieces; this does not establish a need to replace firmware.

No matching module voice runtime files were found under the local
Developer or Downloads directories, or in the checked source trees of
CellBridge, dji-voice-go and DJOneHub.
`AT+QAUDMOD=?` returned `(0-5)` and the earlier read returned mode `1`;
the meanings of these modes for this exact firmware remain unverified.
No module files, audio switches, drivers or firmware were changed by
these initial checks. The missing prerequisite was a compatible voice
driver, calibration service and route runtime, subsequently found in
the public MaVo runtime resources.

To repeat the debug connection check without changing the shared ADB
server, select an unused local port, start a dedicated legacy-USB
server, and inspect its device list:

```sh
ADB_LIBUSB=0 adb -P 5041 devices -l
```

Pin subsequent read-only shell commands with `-P 5041 -t TRANSPORT_ID`
using the verified module transport from that list. Do not assume a
serial-less device is the module without checking its USB location and
device identity. After inspection, stop only the dedicated server with
`adb -P 5041 kill-server`. Setting `ADB_LIBUSB=0` on a client does not
change the USB backend of an already-running server.

## Runtime Recovery and Live Results (2026-11-07)

Public runtime resources were located in
[`moluncn/mavo` at commit `0443dfdaf8aec086fd76ba2ee9152fd908114524`](https://github.com/moluncn/mavo/tree/0443dfdaf8aec086fd76ba2ee9152fd908114524/Resources/ModuleVoice).
Their hashes match the fixed artifacts referenced by CellBridge:

| Artifact | SHA-256 |
| --- | --- |
| `qdc507_aprv3.ko` | `3d82d3dec4f1e323201bba87156df9d41438e08314097353f2607f9117211d4a` |
| `qdc507_voice.ko` | `ed3821682d5309969a01c764192c83feff9669c61ef237c69475cd1619cf296c` |
| `mavo-pcm-bridge.armv7` | `88d47c15e61d1428a59c821fed804c2e6490e82859a085062f21966b58d167fc` |

Before loading, both drivers were checked as ARM ELF32 objects with
vermagic `3.18.44 preempt mod_unload ARMv7 p2v8`, matching the module's
kernel configuration. The files were also hashed again after transfer.
Temporary loading created `mdm9607-tomtom-i2s-snd-card` with VoLTE PCM
device 4 and the USB audio PCM devices 5/6. The existing `alsaucm_test`
service successfully submitted voice calibration. The route helper
then enabled USB audio and entered RUNNING in both directions.

No boot image, firmware or USB layout was changed. Drivers remain
resident until module reboot; they are not hot-unloaded because late
DSP callbacks can race unload. Runtime files are downloaded into the
user cache, copied to `/tmp/dj4hub-voice`, and verified before use.
They are not bundled with the application.

Module voice preparation runs before production host PCM startup.
It uses its own legacy-USB ADB server on a private port,
pins the transport to the verified USB audio location, checks module
identity/kernel/root access, verifies hashes and explicit shell exit
markers, and runs the bounded preparation script. The production bridge
reuses that server across calls and closes it on shutdown or preparation
failure. The standalone `PrepareModuleVoice` helper closes its server on
return. Verified resources are reused without transfer, but each media session
rotates only DJ4Hub's owned route process, identified by
executable and start time. A competing application's calibration or route
process is reported rather than terminated.

After recovery, an authorized 20-second SIP test produced:

- 1,000 RTP packets sent and 990 received.
- 158,400 decoded return samples; 69,183 above the diagnostic threshold,
  with peak amplitude 32,124.
- The tester confirmed hearing the intermittent tone.

The Mac audio callback check selected `MacBook Pro麦克风` and
`MacBook Pro扬声器`, capturing 15,200 samples (15,147 nonzero), playing
17,120 samples with silent input, and dropping none.

A subsequent web/API-initiated call connected the actual Mac microphone
and speakers. Before automatic hangup, counters showed:

- Module capture: 205,312 samples, 201,972 nonzero.
- Module playback: 205,824 samples, 201,696 nonzero; zero drops.
- Mac microphone: 203,200 samples, 202,780 nonzero.
- Mac speaker playback: 204,480 samples, 200,061 nonzero.

These counters preceded voice processing. The tester subsequently confirmed
hearing speech in both directions and reported echo and delay. VoiceProcessingIO
and reduced buffering address those issues; post-change human listening
acceptance is still required. Live incoming-call acceptance and background
microphone permission remain separate checks. Headset devices can be selected
in the local config.

RTP currently binds to and advertises `127.0.0.1` independently of the
SIP listen setting. A LAN/Tailscale client is therefore not a supported
media test until configurable RTP binding and SDP addressing exist.

## Linphone setup (for the MacBook test)

1. Account: SIP on the same Mac, server `127.0.0.1:5060`,
   username `linphone`, password
   from `sip-password`, transport UDP.
2. Codec: PCMU only (G.711 u-law); disable others for the test.
3. Dial a number from Linphone → the server dials it via ATD. An incoming
   call to the module rings Linphone instead of (only) the web UI.

## Layout

- `config.go` — config + first-run password (0600 file).
- `digest.go` — MD5 Digest auth.
- `message.go` — SIP parse/build + SDP (PCMU only).
- `rtp.go` — RTP session (allocate, send silence/bridge PCM, count).
- `g711.go` — mu-law conversion.
- `audio.go` — `AudioBridge` interface + `QPCMVBridge` stub.
- `module_voice.go` / `module_voice_prepare.sh` — verified module runtime provisioning and route preparation.
- `local_call_audio.go` — local web-call audio lifecycle and PCM pumping.
- `server.go` — UDP server, registrations, call state machine, modem hooks.
- `sip_test.go` — unit + loopback UDP flow tests (no modem needed).
