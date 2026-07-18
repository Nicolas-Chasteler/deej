#include <stdlib.h>

// Stubbed out: deej never calls RunWithAppWindow/ShowAppWindow (only plain
// Run(), which passes an empty title so nativeLoop's `configureAppWindow`
// call is skipped). Removing the real GTK+WebKit browser-window
// implementation drops the libwebkit2gtk-4.0 link requirement, which was
// broken by a libjxl soname bump (0.11 -> 0.12) upstream hasn't rebuilt
// against yet.

void configureAppWindow(char* title, int width, int height)
{
    free(title);
}

void showAppWindow(char* url)
{
    free(url);
}
