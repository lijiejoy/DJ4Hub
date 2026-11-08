#ifndef DJ4HUB_NATIVE_AUDIO_H
#define DJ4HUB_NATIVE_AUDIO_H
#include <stdint.h>
#include <stdlib.h>

typedef struct {
    uint32_t input_id, output_id;
    char input_name[256], output_name[256];
    char input_uid[256], output_uid[256];
} dj_audio_devices;

typedef struct {
    uint64_t captured, played, input_nonzero, output_nonzero, dropped, underruns;
    uint64_t capture_queued, playback_queued;
} dj_audio_counts;

typedef struct {
    double sample_rate;
    uint32_t channels, bits, bytes_per_frame, format_flags;
    uint32_t buffer_frames;
    int32_t buffer_status;
    int32_t mute_status[2], volume_status[2];
    uint32_t mute[2];
    float volume[2];
} dj_audio_controls;

typedef struct dj_audio dj_audio;
int dj_audio_permission(char *error, int error_size);
int dj_audio_authorization(void);
int dj_audio_probe(dj_audio_devices *devices, char *error, int error_size);
int dj_audio_inspect(dj_audio_devices *devices, dj_audio_controls *input, dj_audio_controls *output, char *error, int error_size);
dj_audio *dj_audio_open(dj_audio_devices *devices, char *error, int error_size);
void dj_audio_close(dj_audio *audio);
int dj_audio_alive(dj_audio *audio);
int dj_audio_read(dj_audio *audio, int16_t *pcm, int count);
void dj_audio_write(dj_audio *audio, const int16_t *pcm, int count);
void dj_audio_counters(dj_audio *audio, dj_audio_counts *counts);
typedef struct dj_local_audio dj_local_audio;
dj_local_audio *dj_local_audio_open(const char *input_uid, const char *output_uid, int start, dj_audio_devices *devices, char *error, int error_size);

// One Mac audio device that a local call may use. The module's own USB
// audio engine is never listed here: it is the far end of the call, not a
// host microphone/speaker choice.
typedef struct {
    char name[256];
    char uid[256];
    int has_input;
    int has_output;
    int transport; // kAudioDeviceTransportType* value, 0 when unreadable
} dj_local_audio_device;
int dj_local_audio_list(dj_local_audio_device *devices, int capacity, int *count, char *error, int error_size);
int dj_local_audio_start(dj_local_audio *audio);
void dj_local_audio_close(dj_local_audio *audio);
int dj_local_audio_read(dj_local_audio *audio, int16_t *pcm, int count);
void dj_local_audio_write(dj_local_audio *audio, const int16_t *pcm, int count);
void dj_local_audio_counters(dj_local_audio *audio, dj_audio_counts *counts);
int dj_local_audio_alive(dj_local_audio *audio);
#endif
