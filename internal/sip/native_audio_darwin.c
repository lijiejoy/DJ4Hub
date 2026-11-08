//go:build darwin && cgo

#include "native_audio.h"
#include <CoreAudio/CoreAudio.h>
#include <AudioToolbox/AudioToolbox.h>
#include <AudioUnit/AudioUnit.h>
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOKitLib.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

// Single-producer/single-consumer queues keep the realtime callbacks free
// of locks, allocation, Go calls and system property queries.
#define DJ_RING 4096
typedef struct {
    int16_t samples[DJ_RING];
    _Atomic uint64_t read, write;
} dj_ring;

struct dj_audio {
    AudioDeviceID input, output;
    AudioDeviceIOProcID input_proc, output_proc;
    int input_started, output_started;
    UInt32 input_saved_frames, output_saved_frames;
    int input_buffer_changed, output_buffer_changed;
    AudioStreamBasicDescription input_format, output_format;
    dj_ring capture, playback;
    _Atomic uint32_t capture_frames;
    _Atomic uint32_t playback_frames;
    _Atomic uint64_t captured, played, input_nonzero, output_nonzero, dropped, underruns;
};

static AudioObjectPropertyAddress prop(AudioObjectPropertySelector selector, AudioObjectPropertyScope scope) {
    AudioObjectPropertyAddress address = {selector, scope, kAudioObjectPropertyElementMain};
    return address;
}

static int get_string(AudioObjectID device, AudioObjectPropertySelector selector, char *out, int size) {
    CFStringRef value = NULL;
    UInt32 bytes = sizeof(value);
    AudioObjectPropertyAddress address = prop(selector, kAudioObjectPropertyScopeGlobal);
    if (AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, &value) != noErr || !value) return 0;
    int ok = CFStringGetCString(value, out, size, kCFStringEncodingUTF8);
    CFRelease(value);
    return ok;
}

static int get_format(AudioDeviceID device, AudioObjectPropertyScope scope, AudioStreamBasicDescription *format) {
    AudioObjectPropertyAddress address = prop(kAudioDevicePropertyStreamFormat, scope);
    UInt32 bytes = sizeof(*format);
    return AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, format) == noErr;
}

static int usable_format(const AudioStreamBasicDescription *f) {
    if (f->mFormatID != kAudioFormatLinearPCM || f->mSampleRate != 8000 ||
        f->mChannelsPerFrame != 1 || f->mFramesPerPacket != 1 ||
        (f->mFormatFlags & kAudioFormatFlagIsBigEndian)) return 0;
    if ((f->mFormatFlags & kAudioFormatFlagIsFloat) && f->mBitsPerChannel == 32 && f->mBytesPerFrame == 4) return 1;
    return (f->mFormatFlags & kAudioFormatFlagIsSignedInteger) && f->mBitsPerChannel == 16 && f->mBytesPerFrame == 2;
}

// Match the CoreAudio UID against an actual USB engine with the module's
// VID/PID. Names alone are insufficient and defaults are never selected.
static int module_uid(const char *uid, char *prefix, size_t prefix_size) {
    io_iterator_t iterator = 0;
    if (IOServiceGetMatchingServices(kIOMainPortDefault, IOServiceMatching("AppleUSBAudioEngine"), &iterator) != KERN_SUCCESS) return 0;
    int matched = 0;
    io_service_t service;
    while ((service = IOIteratorNext(iterator))) {
        CFTypeRef vendor = IORegistryEntryCreateCFProperty(service, CFSTR("idVendor"), kCFAllocatorDefault, 0);
        CFTypeRef product = IORegistryEntryCreateCFProperty(service, CFSTR("idProduct"), kCFAllocatorDefault, 0);
        CFTypeRef identity = IORegistryEntryCreateCFProperty(service, CFSTR("IOAudioEngineGlobalUniqueID"), kCFAllocatorDefault, 0);
        int vid = 0, pid = 0;
        char engine_uid[256] = {0};
        if (vendor && CFGetTypeID(vendor) == CFNumberGetTypeID()) CFNumberGetValue(vendor, kCFNumberIntType, &vid);
        if (product && CFGetTypeID(product) == CFNumberGetTypeID()) CFNumberGetValue(product, kCFNumberIntType, &pid);
        if (identity && CFGetTypeID(identity) == CFStringGetTypeID()) CFStringGetCString(identity, engine_uid, sizeof(engine_uid), kCFStringEncodingUTF8);
        if (vid == 0x2ca3 && pid == 0x4006 && strcmp(uid, engine_uid) == 0) {
            snprintf(prefix, prefix_size, "%s", uid);
            char *last = strrchr(prefix, ':');
            if (last) *last = '\0';
            matched = 1;
        }
        if (vendor) CFRelease(vendor);
        if (product) CFRelease(product);
        if (identity) CFRelease(identity);
        IOObjectRelease(service);
        if (matched) break;
    }
    IOObjectRelease(iterator);
    return matched;
}

int dj_audio_probe(dj_audio_devices *devices, char *error, int error_size) {
    memset(devices, 0, sizeof(*devices));
    AudioObjectPropertyAddress address = prop(kAudioHardwarePropertyDevices, kAudioObjectPropertyScopeGlobal);
    UInt32 bytes = 0;
    OSStatus status = AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &address, 0, NULL, &bytes);
    if (status != noErr || bytes == 0) {
        snprintf(error, error_size, "cannot enumerate CoreAudio devices (OSStatus %d)", (int)status);
        return -1;
    }
    AudioDeviceID *ids = malloc(bytes);
    if (!ids) { snprintf(error, error_size, "cannot allocate device list"); return -1; }
    status = AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &bytes, ids);
    if (status != noErr) {
        free(ids);
        snprintf(error, error_size, "cannot read CoreAudio devices (OSStatus %d)", (int)status);
        return -1;
    }
    char chosen_prefix[256] = {0};
    int ambiguous = 0;
    for (UInt32 i = 0; i < bytes / sizeof(*ids); i++) {
        char uid[256] = {0}, prefix[256] = {0}, name[256] = {0};
        AudioStreamBasicDescription format;
        if (!get_string(ids[i], kAudioDevicePropertyDeviceUID, uid, sizeof(uid)) ||
            !module_uid(uid, prefix, sizeof(prefix))) continue;
        if (chosen_prefix[0] && strcmp(prefix, chosen_prefix) != 0) { ambiguous = 1; break; }
        snprintf(chosen_prefix, sizeof(chosen_prefix), "%s", prefix);
        get_string(ids[i], kAudioObjectPropertyName, name, sizeof(name));
        if (get_format(ids[i], kAudioObjectPropertyScopeInput, &format) && usable_format(&format)) {
            if (devices->input_id && devices->input_id != ids[i]) { ambiguous = 1; break; }
            devices->input_id = ids[i];
            snprintf(devices->input_uid, sizeof(devices->input_uid), "%s", uid);
            snprintf(devices->input_name, sizeof(devices->input_name), "%s", name);
        }
        if (get_format(ids[i], kAudioObjectPropertyScopeOutput, &format) && usable_format(&format)) {
            if (devices->output_id && devices->output_id != ids[i]) { ambiguous = 1; break; }
            devices->output_id = ids[i];
            snprintf(devices->output_uid, sizeof(devices->output_uid), "%s", uid);
            snprintf(devices->output_name, sizeof(devices->output_name), "%s", name);
        }
    }
    free(ids);
    if (ambiguous || !devices->input_id || !devices->output_id) {
        snprintf(error, error_size, "%s", ambiguous ? "multiple module audio devices; disconnect extra modules" :
                 "no matching 2ca3:4006 USB audio input/output in 8000 Hz mono PCM format");
        return -1;
    }
    return 0;
}

static int inspect_controls(AudioDeviceID device, AudioObjectPropertyScope scope, dj_audio_controls *controls) {
    memset(controls, 0, sizeof(*controls));
    AudioStreamBasicDescription format;
    if (!get_format(device, scope, &format)) return -1;
    controls->sample_rate = format.mSampleRate;
    controls->channels = format.mChannelsPerFrame;
    controls->bits = format.mBitsPerChannel;
    controls->bytes_per_frame = format.mBytesPerFrame;
    controls->format_flags = format.mFormatFlags;
    AudioObjectPropertyAddress buffer_address = prop(kAudioDevicePropertyBufferFrameSize, kAudioObjectPropertyScopeGlobal);
    UInt32 buffer_bytes = sizeof(controls->buffer_frames);
    controls->buffer_status = AudioObjectGetPropertyData(device, &buffer_address, 0, NULL, &buffer_bytes, &controls->buffer_frames);
    for (UInt32 channel = 0; channel < 2; channel++) {
        AudioObjectPropertyAddress address = prop(kAudioDevicePropertyMute, scope);
        address.mElement = channel;
        UInt32 bytes = sizeof(controls->mute[channel]);
        controls->mute_status[channel] = AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, &controls->mute[channel]);
        address.mSelector = kAudioDevicePropertyVolumeScalar;
        bytes = sizeof(controls->volume[channel]);
        controls->volume_status[channel] = AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, &controls->volume[channel]);
    }
    return 0;
}

int dj_audio_inspect(dj_audio_devices *devices, dj_audio_controls *input, dj_audio_controls *output, char *error, int error_size) {
    if (dj_audio_probe(devices, error, error_size)) return -1;
    if (inspect_controls(devices->input_id, kAudioObjectPropertyScopeInput, input) ||
        inspect_controls(devices->output_id, kAudioObjectPropertyScopeOutput, output)) {
        snprintf(error, error_size, "cannot read module audio format during inspection");
        return -1;
    }
    return 0;
}

static int ring_push(dj_ring *ring, int16_t value) {
    uint64_t w = atomic_load_explicit(&ring->write, memory_order_relaxed);
    uint64_t r = atomic_load_explicit(&ring->read, memory_order_acquire);
    if (w - r >= DJ_RING) return 0;
    ring->samples[w % DJ_RING] = value;
    atomic_store_explicit(&ring->write, w + 1, memory_order_release);
    return 1;
}

static int ring_pop(dj_ring *ring, int16_t *value) {
    uint64_t r = atomic_load_explicit(&ring->read, memory_order_relaxed);
    uint64_t w = atomic_load_explicit(&ring->write, memory_order_acquire);
    if (r == w) return 0;
    *value = ring->samples[r % DJ_RING];
    atomic_store_explicit(&ring->read, r + 1, memory_order_release);
    return 1;
}

// Only the consumer moves read. Bound stale audio after scheduling stalls or
// device clock drift instead of making every subsequent frame arrive late.
static void ring_trim(dj_ring *ring, uint64_t keep, _Atomic uint64_t *dropped) {
    uint64_t r = atomic_load_explicit(&ring->read, memory_order_relaxed);
    uint64_t w = atomic_load_explicit(&ring->write, memory_order_acquire);
    if (w - r > keep) {
        atomic_store_explicit(&ring->read, w - keep, memory_order_release);
        atomic_fetch_add_explicit(dropped, w - r - keep, memory_order_relaxed);
    }
}

static uint64_t ring_queued(dj_ring *ring) {
    uint64_t r = atomic_load_explicit(&ring->read, memory_order_acquire);
    uint64_t w = atomic_load_explicit(&ring->write, memory_order_acquire);
    return w - r;
}

static int16_t read_sample(const void *data, UInt32 i, int is_float) {
    if (!is_float) return ((const int16_t *)data)[i];
    float sample = ((const float *)data)[i];
    if (!isfinite(sample)) return 0;
    if (sample >= 1.0f) return 32767;
    if (sample <= -1.0f) return -32768;
    return (int16_t)(sample * 32768.0f);
}

static OSStatus capture_io(AudioObjectID device, const AudioTimeStamp *now,
                          const AudioBufferList *input, const AudioTimeStamp *input_time,
                          AudioBufferList *output, const AudioTimeStamp *output_time, void *context) {
    dj_audio *audio = context;
    if (!input || !input->mNumberBuffers || !input->mBuffers[0].mData) return noErr;
    const AudioBuffer *buffer = &input->mBuffers[0];
    UInt32 count = buffer->mDataByteSize / audio->input_format.mBytesPerFrame;
    if (count > atomic_load_explicit(&audio->capture_frames, memory_order_relaxed))
        atomic_store_explicit(&audio->capture_frames, count, memory_order_relaxed);
    int is_float = !!(audio->input_format.mFormatFlags & kAudioFormatFlagIsFloat);
    for (UInt32 i = 0; i < count; i++) {
        int16_t sample = read_sample(buffer->mData, i, is_float);
        atomic_fetch_add_explicit(&audio->captured, 1, memory_order_relaxed);
        if (sample) atomic_fetch_add_explicit(&audio->input_nonzero, 1, memory_order_relaxed);
        if (!ring_push(&audio->capture, sample)) atomic_fetch_add_explicit(&audio->dropped, 1, memory_order_relaxed);
    }
    return noErr;
}

static OSStatus playback_io(AudioObjectID device, const AudioTimeStamp *now,
                           const AudioBufferList *input, const AudioTimeStamp *input_time,
                           AudioBufferList *output, const AudioTimeStamp *output_time, void *context) {
    dj_audio *audio = context;
    if (!output) return noErr;
    for (UInt32 b = 0; b < output->mNumberBuffers; b++) {
        AudioBuffer *buffer = &output->mBuffers[b];
        if (!buffer->mData) continue;
        memset(buffer->mData, 0, buffer->mDataByteSize);
        if (b != 0) continue;
        UInt32 count = buffer->mDataByteSize / audio->output_format.mBytesPerFrame;
        uint32_t quantum = atomic_load_explicit(&audio->playback_frames, memory_order_relaxed);
        ring_trim(&audio->playback, (quantum > count ? quantum : count) + 160, &audio->dropped);
        int is_float = !!(audio->output_format.mFormatFlags & kAudioFormatFlagIsFloat);
        for (UInt32 i = 0; i < count; i++) {
            int16_t sample = 0;
            if (!ring_pop(&audio->playback, &sample)) atomic_fetch_add_explicit(&audio->underruns, 1, memory_order_relaxed);
            if (is_float) ((float *)buffer->mData)[i] = (float)sample / 32768.0f;
            else ((int16_t *)buffer->mData)[i] = sample;
            atomic_fetch_add_explicit(&audio->played, 1, memory_order_relaxed);
            if (sample) atomic_fetch_add_explicit(&audio->output_nonzero, 1, memory_order_relaxed);
        }
    }
    return noErr;
}

static void low_latency_buffer(AudioDeviceID device, UInt32 *saved, int *changed) {
    AudioObjectPropertyAddress address = prop(kAudioDevicePropertyBufferFrameSize, kAudioObjectPropertyScopeGlobal);
    UInt32 bytes = sizeof(*saved);
    Boolean writable = false;
    if (AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, saved) != noErr || *saved <= 128 ||
        AudioObjectIsPropertySettable(device, &address, &writable) != noErr || !writable) return;
    AudioObjectPropertyAddress range_address = prop(kAudioDevicePropertyBufferFrameSizeRange, kAudioObjectPropertyScopeGlobal);
    AudioValueRange range;
    bytes = sizeof(range);
    if (AudioObjectGetPropertyData(device, &range_address, 0, NULL, &bytes, &range) != noErr ||
        range.mMinimum > 128 || range.mMaximum < 128) return;
    UInt32 frames = 128;
    if (AudioObjectSetPropertyData(device, &address, 0, NULL, sizeof(frames), &frames) == noErr) *changed = 1;
}

static void restore_buffer(AudioDeviceID device, UInt32 saved, int changed) {
    if (!changed) return;
    AudioObjectPropertyAddress address = prop(kAudioDevicePropertyBufferFrameSize, kAudioObjectPropertyScopeGlobal);
    UInt32 current = 0, bytes = sizeof(current);
    // Preserve a setting another application changed during the call.
    if (AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, &current) == noErr && current == 128)
        AudioObjectSetPropertyData(device, &address, 0, NULL, sizeof(saved), &saved);
}

void dj_audio_close(dj_audio *audio) {
    if (!audio) return;
    if (audio->input_started) AudioDeviceStop(audio->input, audio->input_proc);
    if (audio->output_started) AudioDeviceStop(audio->output, audio->output_proc);
    if (audio->input_proc) AudioDeviceDestroyIOProcID(audio->input, audio->input_proc);
    if (audio->output_proc) AudioDeviceDestroyIOProcID(audio->output, audio->output_proc);
    restore_buffer(audio->input, audio->input_saved_frames, audio->input_buffer_changed);
    restore_buffer(audio->output, audio->output_saved_frames, audio->output_buffer_changed);
    free(audio);
}

dj_audio *dj_audio_open(dj_audio_devices *devices, char *error, int error_size) {
    if (dj_audio_probe(devices, error, error_size)) return NULL;
    dj_audio *audio = calloc(1, sizeof(*audio));
    if (!audio) { snprintf(error, error_size, "cannot allocate audio buffers"); return NULL; }
    audio->input = devices->input_id;
    audio->output = devices->output_id;
    if (!get_format(audio->input, kAudioObjectPropertyScopeInput, &audio->input_format) ||
        !get_format(audio->output, kAudioObjectPropertyScopeOutput, &audio->output_format) ||
        !usable_format(&audio->input_format) || !usable_format(&audio->output_format)) {
        snprintf(error, error_size, "USB audio format changed before stream startup");
        dj_audio_close(audio); return NULL;
    }
    low_latency_buffer(audio->input, &audio->input_saved_frames, &audio->input_buffer_changed);
    low_latency_buffer(audio->output, &audio->output_saved_frames, &audio->output_buffer_changed);
    OSStatus status = AudioDeviceCreateIOProcID(audio->input, capture_io, audio, &audio->input_proc);
    const char *stage = "create capture callback";
    if (status == noErr) { stage = "create playback callback"; status = AudioDeviceCreateIOProcID(audio->output, playback_io, audio, &audio->output_proc); }
    if (status == noErr) {
        stage = "start USB audio playback";
        status = AudioDeviceStart(audio->output, audio->output_proc);
        audio->output_started = status == noErr;
    }
    if (status == noErr) {
        stage = "start USB audio capture (check macOS Microphone permission)";
        status = AudioDeviceStart(audio->input, audio->input_proc);
        audio->input_started = status == noErr;
    }
    if (status != noErr) {
        snprintf(error, error_size, "%s: OSStatus %d", stage, (int)status);
        dj_audio_close(audio); return NULL;
    }
    return audio;
}

static int device_alive(AudioDeviceID device, AudioObjectPropertyScope scope, const AudioStreamBasicDescription *original) {
    UInt32 alive = 0, bytes = sizeof(alive);
    AudioObjectPropertyAddress address = prop(kAudioDevicePropertyDeviceIsAlive, kAudioObjectPropertyScopeGlobal);
    AudioStreamBasicDescription current;
    return AudioObjectGetPropertyData(device, &address, 0, NULL, &bytes, &alive) == noErr && alive &&
        get_format(device, scope, &current) && usable_format(&current) &&
        current.mFormatFlags == original->mFormatFlags && current.mBytesPerFrame == original->mBytesPerFrame;
}

int dj_audio_alive(dj_audio *audio) {
    return audio && device_alive(audio->input, kAudioObjectPropertyScopeInput, &audio->input_format) &&
        device_alive(audio->output, kAudioObjectPropertyScopeOutput, &audio->output_format);
}

int dj_audio_read(dj_audio *audio, int16_t *pcm, int count) {
    int read = 0;
    uint32_t quantum = atomic_load_explicit(&audio->capture_frames, memory_order_relaxed);
    ring_trim(&audio->capture, (quantum > count ? quantum : count) + 160, &audio->dropped);
    while (read < count && ring_pop(&audio->capture, &pcm[read])) read++;
    return read;
}

void dj_audio_write(dj_audio *audio, const int16_t *pcm, int count) {
    if (count > atomic_load_explicit(&audio->playback_frames, memory_order_relaxed))
        atomic_store_explicit(&audio->playback_frames, count, memory_order_relaxed);
    for (int i = 0; i < count; i++)
        if (!ring_push(&audio->playback, pcm[i])) atomic_fetch_add_explicit(&audio->dropped, 1, memory_order_relaxed);
}

void dj_audio_counters(dj_audio *audio, dj_audio_counts *counts) {
    counts->capture_queued = ring_queued(&audio->capture);
    counts->playback_queued = ring_queued(&audio->playback);
    counts->captured = atomic_load_explicit(&audio->captured, memory_order_relaxed);
    counts->played = atomic_load_explicit(&audio->played, memory_order_relaxed);
    counts->input_nonzero = atomic_load_explicit(&audio->input_nonzero, memory_order_relaxed);
    counts->output_nonzero = atomic_load_explicit(&audio->output_nonzero, memory_order_relaxed);
    counts->dropped = atomic_load_explicit(&audio->dropped, memory_order_relaxed);
    counts->underruns = atomic_load_explicit(&audio->underruns, memory_order_relaxed);
}

struct dj_local_audio {
    AudioUnit voice;
    int initialized, started;
    int16_t input_samples[4096];
    AudioDeviceID input_id, output_id;
    dj_ring capture, playback;
    _Atomic uint32_t capture_frames;
    _Atomic uint32_t playback_frames;
    _Atomic uint64_t captured, played, input_nonzero, output_nonzero, dropped, underruns;
    _Atomic int failed;
};

static AudioDeviceID local_device(AudioObjectPropertyScope scope, const char *wanted, char *uid, char *name) {
    AudioObjectPropertyAddress address = prop(kAudioHardwarePropertyDevices, kAudioObjectPropertyScopeGlobal);
    AudioDeviceID ids[128];
    UInt32 bytes = sizeof(ids);
    if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &bytes, ids) != noErr) return 0;
    for (UInt32 i = 0; i < bytes / sizeof(*ids); i++) {
        char candidate_uid[256] = {0}, candidate_name[256] = {0}, prefix[256];
        if (!get_string(ids[i], kAudioDevicePropertyDeviceUID, candidate_uid, sizeof(candidate_uid)) ||
            module_uid(candidate_uid, prefix, sizeof(prefix))) continue;
        get_string(ids[i], kAudioObjectPropertyName, candidate_name, sizeof(candidate_name));
        if (wanted[0]) {
            if (strcmp(candidate_uid, wanted) && strcmp(candidate_name, wanted)) continue;
        } else {
            UInt32 transport = 0, size = sizeof(transport);
            address = prop(kAudioDevicePropertyTransportType, kAudioObjectPropertyScopeGlobal);
            if (AudioObjectGetPropertyData(ids[i], &address, 0, NULL, &size, &transport) != noErr ||
                transport != kAudioDeviceTransportTypeBuiltIn) continue;
        }
        address = prop(kAudioDevicePropertyStreams, scope);
        UInt32 size = 0;
        if (AudioObjectGetPropertyDataSize(ids[i], &address, 0, NULL, &size) != noErr || !size) continue;
        snprintf(uid, 256, "%s", candidate_uid);
        snprintf(name, 256, "%s", candidate_name);
        return ids[i];
    }
    return 0;
}

static OSStatus local_capture(void *context, AudioUnitRenderActionFlags *flags,
                              const AudioTimeStamp *time, UInt32 bus, UInt32 frames, AudioBufferList *unused) {
    dj_local_audio *audio = context;
    if (frames > 4096) {
        atomic_store(&audio->failed, kAudio_ParamError);
        return kAudio_ParamError;
    }
    AudioBufferList input = { .mNumberBuffers = 1,
        .mBuffers = {{ .mNumberChannels = 1, .mDataByteSize = frames * 2, .mData = audio->input_samples }} };
    OSStatus result = AudioUnitRender(audio->voice, flags, time, 1, frames, &input);
    if (result != noErr) { atomic_store(&audio->failed, result); return result; }
    if (frames > atomic_load_explicit(&audio->capture_frames, memory_order_relaxed))
        atomic_store_explicit(&audio->capture_frames, frames, memory_order_relaxed);
    const int16_t *pcm = audio->input_samples;
    for (UInt32 i = 0; i < frames; i++) {
        atomic_fetch_add_explicit(&audio->captured, 1, memory_order_relaxed);
        if (pcm[i]) atomic_fetch_add_explicit(&audio->input_nonzero, 1, memory_order_relaxed);
        if (!ring_push(&audio->capture, pcm[i])) atomic_fetch_add_explicit(&audio->dropped, 1, memory_order_relaxed);
    }
    return noErr;
}

static OSStatus local_playback(void *context, AudioUnitRenderActionFlags *flags,
                               const AudioTimeStamp *time, UInt32 bus, UInt32 frames, AudioBufferList *output) {
    dj_local_audio *audio = context;
    if (!output || !output->mNumberBuffers || !output->mBuffers[0].mData) return noErr;
    for (UInt32 b = 0; b < output->mNumberBuffers; b++)
        if (output->mBuffers[b].mData) memset(output->mBuffers[b].mData, 0, output->mBuffers[b].mDataByteSize);
    int16_t *pcm = output->mBuffers[0].mData;
    UInt32 count = output->mBuffers[0].mDataByteSize / 2;
    if (count > frames) count = frames;
    uint32_t quantum = atomic_load_explicit(&audio->playback_frames, memory_order_relaxed);
    ring_trim(&audio->playback, (quantum > count ? quantum : count) + 160, &audio->dropped);
    for (UInt32 i = 0; i < count; i++) {
        int16_t sample = 0;
        if (!ring_pop(&audio->playback, &sample)) atomic_fetch_add_explicit(&audio->underruns, 1, memory_order_relaxed);
        pcm[i] = sample;
        atomic_fetch_add_explicit(&audio->played, 1, memory_order_relaxed);
        if (sample) atomic_fetch_add_explicit(&audio->output_nonzero, 1, memory_order_relaxed);
    }
    return noErr;
}

void dj_local_audio_close(dj_local_audio *audio) {
    if (!audio) return;
    if (audio->started) AudioOutputUnitStop(audio->voice);
    if (audio->initialized) AudioUnitUninitialize(audio->voice);
    if (audio->voice) AudioComponentInstanceDispose(audio->voice);
    free(audio);
}

int dj_local_audio_start(dj_local_audio *audio) {
    if (audio->started) return noErr;
    OSStatus result = AudioOutputUnitStart(audio->voice);
    audio->started = result == noErr;
    return result;
}

dj_local_audio *dj_local_audio_open(const char *input_uid, const char *output_uid, int start, dj_audio_devices *devices, char *error, int error_size) {
    memset(devices, 0, sizeof(*devices));
    devices->input_id = local_device(kAudioObjectPropertyScopeInput, input_uid, devices->input_uid, devices->input_name);
    devices->output_id = local_device(kAudioObjectPropertyScopeOutput, output_uid, devices->output_uid, devices->output_name);
    if (!devices->input_id || !devices->output_id) {
        snprintf(error, error_size, "cannot find configured or built-in Mac microphone/speakers");
        return NULL;
    }
    dj_local_audio *audio = calloc(1, sizeof(*audio));
    if (!audio) { snprintf(error, error_size, "cannot allocate local audio"); return NULL; }
    audio->input_id = devices->input_id;
    audio->output_id = devices->output_id;
    // The same unit renders far-end audio and captures processed near-end
    // audio, so its echo canceller has the actual speaker reference.
    AudioStreamBasicDescription format = {0};
    format.mSampleRate = 8000;
    format.mFormatID = kAudioFormatLinearPCM;
    format.mFormatFlags = kAudioFormatFlagIsSignedInteger | kAudioFormatFlagIsPacked;
    format.mBytesPerPacket = format.mBytesPerFrame = 2;
    format.mFramesPerPacket = format.mChannelsPerFrame = 1;
    format.mBitsPerChannel = 16;
    AudioComponentDescription description = { .componentType = kAudioUnitType_Output,
        .componentSubType = kAudioUnitSubType_VoiceProcessingIO, .componentManufacturer = kAudioUnitManufacturer_Apple };
    AudioComponent component = AudioComponentFindNext(NULL, &description);
    OSStatus result = component ? AudioComponentInstanceNew(component, &audio->voice) : kAudio_ParamError;
    UInt32 enabled = 1, bypass = 0, max_frames = 4096;
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &enabled, sizeof(enabled));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0, &audio->output_id, sizeof(audio->output_id));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 1, &audio->input_id, sizeof(audio->input_id));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &format, sizeof(format));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, &format, sizeof(format));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioUnitProperty_MaximumFramesPerSlice, kAudioUnitScope_Global, 0, &max_frames, sizeof(max_frames));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAUVoiceIOProperty_BypassVoiceProcessing, kAudioUnitScope_Global, 0, &bypass, sizeof(bypass));
    AURenderCallbackStruct capture = {local_capture, audio}, playback = {local_playback, audio};
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 1, &capture, sizeof(capture));
    if (result == noErr) result = AudioUnitSetProperty(audio->voice, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &playback, sizeof(playback));
    if (result == noErr) {
        result = AudioUnitInitialize(audio->voice);
        audio->initialized = result == noErr;
    }
    if (result == noErr) {
        UInt32 size = sizeof(bypass);
        result = AudioUnitGetProperty(audio->voice, kAUVoiceIOProperty_BypassVoiceProcessing, kAudioUnitScope_Global, 0, &bypass, &size);
        if (result == noErr && bypass) result = kAudio_ParamError;
    }
    if (result == noErr && start) result = dj_local_audio_start(audio);
    if (result != noErr) {
        snprintf(error, error_size, "start Mac echo-cancelled voice audio: OSStatus %d", (int)result);
        dj_local_audio_close(audio);
        return NULL;
    }
    return audio;
}

int dj_local_audio_read(dj_local_audio *audio, int16_t *pcm, int count) {
    int n = 0;
    uint32_t quantum = atomic_load_explicit(&audio->capture_frames, memory_order_relaxed);
    ring_trim(&audio->capture, (quantum > count ? quantum : count) + 160, &audio->dropped);
    while (n < count && ring_pop(&audio->capture, &pcm[n])) n++;
    return n;
}

void dj_local_audio_write(dj_local_audio *audio, const int16_t *pcm, int count) {
    if (count > atomic_load_explicit(&audio->playback_frames, memory_order_relaxed))
        atomic_store_explicit(&audio->playback_frames, count, memory_order_relaxed);
    for (int i = 0; i < count; i++)
        if (!ring_push(&audio->playback, pcm[i])) atomic_fetch_add_explicit(&audio->dropped, 1, memory_order_relaxed);
}

void dj_local_audio_counters(dj_local_audio *audio, dj_audio_counts *counts) {
    counts->capture_queued = ring_queued(&audio->capture);
    counts->playback_queued = ring_queued(&audio->playback);
    counts->captured = atomic_load(&audio->captured);
    counts->played = atomic_load(&audio->played);
    counts->input_nonzero = atomic_load(&audio->input_nonzero);
    counts->output_nonzero = atomic_load(&audio->output_nonzero);
    counts->dropped = atomic_load(&audio->dropped);
    counts->underruns = atomic_load(&audio->underruns);
}

int dj_local_audio_alive(dj_local_audio *audio) {
    if (atomic_load(&audio->failed)) return 0;
    AudioDeviceID ids[] = {audio->input_id, audio->output_id};
    for (int i = 0; i < 2; i++) {
        UInt32 alive = 0, bytes = sizeof(alive);
        AudioObjectPropertyAddress address = prop(kAudioDevicePropertyDeviceIsAlive, kAudioObjectPropertyScopeGlobal);
        if (AudioObjectGetPropertyData(ids[i], &address, 0, NULL, &bytes, &alive) != noErr || !alive) return 0;
    }
    return 1;
}
