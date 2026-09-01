/*
 * CI-only Security.framework symbol stubs for Darling runs
 * (scripts/darling-ci.sh). Never part of macOS builds.
 *
 * Go 1.24+ targets macOS 12 and dyld-binds Security.framework symbols that
 * Darling's macOS 11.7-era framework lacks, so every binary importing
 * crypto/x509 aborts at load with "Symbol not found". This dylib is
 * cross-linked with zig, injected via DYLD_INSERT_LIBRARIES, and resolved
 * via DYLD_FORCE_FLAT_NAMESPACE (the imports are two-level-bound to
 * Security.framework, which an inserted library cannot otherwise satisfy).
 *
 * Go's crypto/x509 does call these while loading system roots, so the
 * stubs are implemented in terms of the older macOS 11 APIs they
 * superseded (the same fallback Go itself used before requiring macOS 12).
 * The extern symbols resolve at load time against the real frameworks via
 * -undefined dynamic_lookup.
 */

extern long SecTrustGetCertificateCount(void *trust);
extern void *SecTrustGetCertificateAtIndex(void *trust, long i);
extern void *CFArrayCreateMutable(void *allocator, long capacity, const void *callbacks);
extern void CFArrayAppendValue(void *array, const void *value);
extern const char kCFTypeArrayCallBacks[];

/* CFArrayRef SecTrustCopyCertificateChain(SecTrustRef); macOS 12.0+ */
void *SecTrustCopyCertificateChain(void *trust) {
	if (!trust) {
		return 0;
	}
	long n = SecTrustGetCertificateCount(trust);
	void *arr = CFArrayCreateMutable(0, n, kCFTypeArrayCallBacks);
	if (!arr) {
		return 0;
	}
	for (long i = 0; i < n; i++) {
		void *cert = SecTrustGetCertificateAtIndex(trust, i);
		if (cert) {
			CFArrayAppendValue(arr, cert);
		}
	}
	return arr;
}
