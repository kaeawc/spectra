//go:build darwin && arm64 && cgo

package sysinfo

/*
#cgo LDFLAGS: -framework CoreFoundation

#include <CoreFoundation/CoreFoundation.h>
#include <dlfcn.h>
#include <pthread.h>
#include <stdint.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

// IOReport is dlopen'd at runtime — the framework is private (no SDK
// headers) and a renamed symbol on a future macOS should degrade to
// ErrUnsupportedHardware rather than fail to link.
typedef struct __IOReportSubscription *IOReportSubscriptionRef;

typedef CFMutableDictionaryRef (*pIOReportCopyChannelsInGroup)(CFStringRef, CFStringRef, uint64_t, uint64_t, uint64_t);
typedef IOReportSubscriptionRef (*pIOReportCreateSubscription)(void *, CFMutableDictionaryRef, CFMutableDictionaryRef *, uint64_t, CFTypeRef);
typedef CFDictionaryRef (*pIOReportCreateSamples)(IOReportSubscriptionRef, CFMutableDictionaryRef, CFTypeRef);
typedef CFDictionaryRef (*pIOReportCreateSamplesDelta)(CFDictionaryRef, CFDictionaryRef, CFTypeRef);
typedef int64_t (*pIOReportSimpleGetIntegerValue)(CFDictionaryRef, int);
typedef CFStringRef (*pIOReportChannelGetGroup)(CFDictionaryRef);
typedef CFStringRef (*pIOReportChannelGetChannelName)(CFDictionaryRef);
typedef CFStringRef (*pIOReportChannelGetUnitLabel)(CFDictionaryRef);

typedef struct {
	int64_t cpu_p_nj;
	int64_t cpu_e_nj;
	int64_t gpu_nj;
	int64_t ane_nj;
	int64_t dram_nj;
	int64_t package_nj;
	int64_t actual_interval_us;
	int     error_code; // 0 ok; 1 dlopen; 2 dlsym; 3 channels; 4 sub; 5 sample
} spectra_soc_power_t;

static void *gIOReportHandle = NULL;
static pIOReportCopyChannelsInGroup    gCopyChans = NULL;
static pIOReportCreateSubscription     gCreateSub = NULL;
static pIOReportCreateSamples          gCreateSamples = NULL;
static pIOReportCreateSamplesDelta     gCreateDelta = NULL;
static pIOReportSimpleGetIntegerValue  gGetInt = NULL;
static pIOReportChannelGetGroup        gGetGroup = NULL;
static pIOReportChannelGetChannelName  gGetChanName = NULL;
static pIOReportChannelGetUnitLabel    gGetUnit = NULL;

// macOS 15+ exposes IOReport as /usr/lib/libIOReport.dylib (resolved via
// the dyld shared cache); older macOS keeps it as a PrivateFrameworks
// bundle. Trying both lets one binary serve both.
static const char *kIOReportPaths[] = {
	"libIOReport.dylib",
	"/usr/lib/libIOReport.dylib",
	"/System/Library/PrivateFrameworks/IOReport.framework/IOReport",
	NULL,
};

static pthread_once_t gIOReportOnce = PTHREAD_ONCE_INIT;
static int gIOReportLoadResult = -1;

static void spectra_ioreport_init(void) {
	for (int i = 0; kIOReportPaths[i] != NULL; i++) {
		gIOReportHandle = dlopen(kIOReportPaths[i], RTLD_LAZY);
		if (gIOReportHandle != NULL) break;
	}
	if (gIOReportHandle == NULL) { gIOReportLoadResult = 1; return; }
	gCopyChans = (pIOReportCopyChannelsInGroup)dlsym(gIOReportHandle, "IOReportCopyChannelsInGroup");
	gCreateSub = (pIOReportCreateSubscription)dlsym(gIOReportHandle, "IOReportCreateSubscription");
	gCreateSamples = (pIOReportCreateSamples)dlsym(gIOReportHandle, "IOReportCreateSamples");
	gCreateDelta = (pIOReportCreateSamplesDelta)dlsym(gIOReportHandle, "IOReportCreateSamplesDelta");
	gGetInt = (pIOReportSimpleGetIntegerValue)dlsym(gIOReportHandle, "IOReportSimpleGetIntegerValue");
	gGetGroup = (pIOReportChannelGetGroup)dlsym(gIOReportHandle, "IOReportChannelGetGroup");
	gGetChanName = (pIOReportChannelGetChannelName)dlsym(gIOReportHandle, "IOReportChannelGetChannelName");
	gGetUnit = (pIOReportChannelGetUnitLabel)dlsym(gIOReportHandle, "IOReportChannelGetUnitLabel");
	if (!gCopyChans || !gCreateSub || !gCreateSamples || !gCreateDelta ||
	    !gGetInt || !gGetGroup || !gGetChanName || !gGetUnit) {
		gIOReportLoadResult = 2;
		return;
	}
	gIOReportLoadResult = 0;
}

static int spectra_ioreport_load(void) {
	pthread_once(&gIOReportOnce, spectra_ioreport_init);
	return gIOReportLoadResult;
}

static void cf_to_cstring(CFStringRef s, char *buf, int bufsz) {
	buf[0] = '\0';
	if (s == NULL) return;
	CFStringGetCString(s, buf, bufsz, kCFStringEncodingUTF8);
}

// unit_to_nj returns the multiplier that converts a value in the given unit
// label to nanojoules. Returns 0 for non-energy units so we drop them rather
// than mistakenly sum milliwatts or unit-less counters.
static int64_t unit_to_nj(const char *unit) {
	if (unit == NULL || unit[0] == '\0') return 0;
	if (strcmp(unit, "nJ") == 0) return 1;
	if (strcmp(unit, "uJ") == 0) return 1000;
	if (strcmp(unit, "mJ") == 0) return 1000000;
	if (strcmp(unit, "J") == 0)  return 1000000000;
	return 0;
}

// classify_and_add accumulates an energy delta (already converted to
// nanojoules) into the right subsystem bucket using exact channel-name
// matches.
//
// IOReport "Energy Model" exposes both rollup channels and fine-grained
// per-core / SRAM detail channels (PCPUDTL*, *_SRAM, EACC_CPU0…). Summing
// everything double-counts; instead we whitelist the canonical rollups:
//
//   - CPU total contributes to Package via the "CPU Energy" rollup.
//   - CPU E split = EACC_CPU; CPU P split = sum(PACC<N>_CPU).
//   - GPU = "GPU Energy" (already a rollup over GPU0 / CS / SRAM).
//   - ANE = "ANE0"; DRAM = "DRAM0".
static void classify_and_add(spectra_soc_power_t *out, const char *name, int64_t nj) {
	if (nj <= 0) return;
	if (strcmp(name, "CPU Energy") == 0) {
		out->package_nj += nj;
		return;
	}
	if (strcmp(name, "GPU Energy") == 0) {
		out->gpu_nj += nj;
		out->package_nj += nj;
		return;
	}
	if (strcmp(name, "ANE0") == 0) {
		out->ane_nj += nj;
		out->package_nj += nj;
		return;
	}
	if (strcmp(name, "DRAM0") == 0) {
		out->dram_nj += nj;
		out->package_nj += nj;
		return;
	}
	if (strcmp(name, "EACC_CPU") == 0) {
		out->cpu_e_nj += nj;
		return;
	}
	// "PACC_CPU" on single-P-cluster chips (M1), "PACC0_CPU"/"PACC1_CPU" on
	// multi-cluster chips (M-Pro/Max/Ultra). Exclude PACC*_CPM (cluster power
	// manager), PACC*_CPU<n>, PACC*_CPU*_SRAM, etc.
	size_t n = strlen(name);
	if (strncmp(name, "PACC", 4) == 0 && n >= 8 && strcmp(name + n - 4, "_CPU") == 0) {
		int mid_ok = 1;
		for (size_t k = 4; k < n - 4; k++) {
			if (name[k] < '0' || name[k] > '9') { mid_ok = 0; break; }
		}
		if (mid_ok) {
			out->cpu_p_nj += nj;
			return;
		}
	}
}

static void extract_delta(CFDictionaryRef delta, spectra_soc_power_t *out) {
	CFTypeRef channelsRef = CFDictionaryGetValue(delta, CFSTR("IOReportChannels"));
	if (channelsRef == NULL) return;
	if (CFGetTypeID(channelsRef) != CFArrayGetTypeID()) return;
	CFArrayRef channels = (CFArrayRef)channelsRef;
	CFIndex count = CFArrayGetCount(channels);
	char nameBuf[128];
	char groupBuf[64];
	char unitBuf[16];
	for (CFIndex i = 0; i < count; i++) {
		CFDictionaryRef item = (CFDictionaryRef)CFArrayGetValueAtIndex(channels, i);
		if (item == NULL) continue;
		cf_to_cstring(gGetGroup(item), groupBuf, sizeof(groupBuf));
		if (strcmp(groupBuf, "Energy Model") != 0) continue;
		cf_to_cstring(gGetUnit(item), unitBuf, sizeof(unitBuf));
		int64_t mult = unit_to_nj(unitBuf);
		if (mult == 0) continue;
		cf_to_cstring(gGetChanName(item), nameBuf, sizeof(nameBuf));
		int64_t v = gGetInt(item, 0);
		classify_and_add(out, nameBuf, v * mult);
	}
}

static int64_t monotonic_us(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (int64_t)ts.tv_sec * 1000000 + (int64_t)ts.tv_nsec / 1000;
}

static void spectra_sample_soc_power(uint64_t interval_us, spectra_soc_power_t *out) {
	memset(out, 0, sizeof(*out));
	int rc = spectra_ioreport_load();
	if (rc != 0) { out->error_code = rc; return; }

	CFMutableDictionaryRef chans = gCopyChans(CFSTR("Energy Model"), NULL, 0, 0, 0);
	if (chans == NULL) { out->error_code = 3; return; }

	CFMutableDictionaryRef subChans = NULL;
	CFDictionaryRef s1 = NULL, s2 = NULL, delta = NULL;
	IOReportSubscriptionRef sub = gCreateSub(NULL, chans, &subChans, 0, NULL);
	if (sub == NULL) { out->error_code = 4; goto cleanup; }

	CFMutableDictionaryRef sampleChans = subChans ? subChans : chans;
	s1 = gCreateSamples(sub, sampleChans, NULL);
	if (s1 == NULL) { out->error_code = 5; goto cleanup; }

	int64_t t0 = monotonic_us();
	usleep((useconds_t)interval_us);

	s2 = gCreateSamples(sub, sampleChans, NULL);
	if (s2 == NULL) { out->error_code = 5; goto cleanup; }
	out->actual_interval_us = monotonic_us() - t0;

	delta = gCreateDelta(s1, s2, NULL);
	if (delta != NULL) extract_delta(delta, out);
	out->error_code = 0;

cleanup:
	if (delta) CFRelease(delta);
	if (s2) CFRelease(s2);
	if (s1) CFRelease(s1);
	if (subChans) CFRelease(subChans);
	CFRelease(chans);
}
*/
import "C"

import (
	"fmt"
	"time"
)

const nanojoulesPerJoule = 1e9

// SampleSoCPower takes two IOReport "Energy Model" samples Δ apart and
// returns the joules consumed by each SoC subsystem. Requires no privileges
// and uses the private IOReport framework via dlopen.
//
// Returns ErrUnsupportedHardware on macOS versions where the IOReport
// framework is missing or its ABI has changed incompatibly.
func SampleSoCPower(d time.Duration) (SoCPower, error) {
	if d <= 0 {
		return SoCPower{}, fmt.Errorf("interval must be positive, got %s", d)
	}
	var c C.spectra_soc_power_t
	C.spectra_sample_soc_power(C.uint64_t(d.Microseconds()), &c)
	switch c.error_code {
	case 0:
		// ok
	case 1, 2:
		return SoCPower{}, ErrUnsupportedHardware
	default:
		return SoCPower{}, fmt.Errorf("IOReport sampling failed (code=%d)", int(c.error_code))
	}
	return SoCPower{
		Interval:      time.Duration(int64(c.actual_interval_us)) * time.Microsecond,
		CPUPJoules:    float64(c.cpu_p_nj) / nanojoulesPerJoule,
		CPUEJoules:    float64(c.cpu_e_nj) / nanojoulesPerJoule,
		GPUJoules:     float64(c.gpu_nj) / nanojoulesPerJoule,
		ANEJoules:     float64(c.ane_nj) / nanojoulesPerJoule,
		DRAMJoules:    float64(c.dram_nj) / nanojoulesPerJoule,
		PackageJoules: float64(c.package_nj) / nanojoulesPerJoule,
	}, nil
}
