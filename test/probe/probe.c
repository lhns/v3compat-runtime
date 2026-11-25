// probe MODE [PATH]
//   translated   AVX2, FMA and BMI2 results are right and DynamoRIO is mapped
//   native       the same results and DynamoRIO is not mapped
//   cache-ro     PATH is a directory on a read-only mount (the translation cache)
//   cache-none   PATH does not exist
//   env          print NAME (the second argument) from the environment, "(unset)" if absent
//   wait         sleep until killed (a long-running container to exec into)
// Without a mode only the results are checked. Exit 0 on success.
#include <errno.h>
#include <fcntl.h>
#include <immintrin.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

static int dynamorio_mapped(void) {
    FILE *f = fopen("/proc/self/maps", "r");
    char line[4096];
    int found = 0;
    if (!f) return -1;
    while (fgets(line, sizeof line, f))
        if (strstr(line, "libdynamorio")) found = 1;
    fclose(f);
    return found;
}

static int results(void) {
    volatile int a = 3, b = 4;
    __m256i v = _mm256_add_epi32(_mm256_set1_epi32(a), _mm256_set1_epi32(b));
    int avx2 = _mm256_extract_epi32(v, 7);

    volatile double x = 2.0, y = 3.0, z = 4.0;
    __m256d d = _mm256_fmadd_pd(_mm256_set1_pd(x), _mm256_set1_pd(y), _mm256_set1_pd(z));
    double fma = _mm256_cvtsd_f64(d);

    volatile uint64_t src = 0xff, mask = 0xf0f0;
    uint64_t bmi2 = _pdep_u64(src, mask);

    int ok = avx2 == 7 && fma == 10.0 && bmi2 == 0xf0f0;
    printf("avx2=%d fma=%g bmi2=%#llx %s\n", avx2, fma, (unsigned long long)bmi2, ok ? "ok" : "WRONG");
    return ok;
}

int main(int argc, char **argv) {
    const char *mode = argc > 1 ? argv[1] : "";
    if (!strcmp(mode, "wait")) for (;;) pause();
    if (!strcmp(mode, "env")) {
        const char *name = argc > 2 ? argv[2] : "", *v = getenv(name);
        printf("%s=%s\n", name, v ? v : "(unset)");
        return 0;
    }
    if (!strcmp(mode, "cache-ro") || !strcmp(mode, "cache-none")) {
        const char *path = argc > 2 ? argv[2] : "";
        struct stat st;
        int exists = stat(path, &st) == 0;
        if (!strcmp(mode, "cache-none")) {
            printf("%s exists=%d\n", path, exists);
            return exists;
        }
        char file[4096];
        snprintf(file, sizeof file, "%s/probe-write", path);
        int fd = open(file, O_CREAT | O_WRONLY, 0600);
        int err = fd < 0 ? errno : 0;
        if (fd >= 0) { close(fd); unlink(file); }
        printf("%s dir=%d write errno=%d\n", path, exists && S_ISDIR(st.st_mode), err);
        return !(exists && S_ISDIR(st.st_mode) && err == EROFS);
    }
    int ok = results();
    int maps = dynamorio_mapped();
    printf("libdynamorio mapped=%d\n", maps);
    if (!strcmp(mode, "translated")) ok = ok && maps == 1;
    else if (!strcmp(mode, "native")) ok = ok && maps == 0;
    return ok ? 0 : 1;
}
