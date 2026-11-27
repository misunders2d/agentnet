#include "ui/grouped_layout.h"
#include <cstdio>
#include <cstdlib>
// Reads cases from stdin: count then w h pairs; prints JSON lines of geometry.
int main() {
 int n;
 bool first = true;
 std::printf("[");
 while (std::scanf("%d", &n) == 1) {
  std::vector<QSize> sizes;
  for (int i = 0; i < n; i++) { int w, h; std::scanf("%d %d", &w, &h); sizes.push_back({w, h}); }
  auto layout = Ui::LayoutMediaGroup(sizes, 430, 100, 4);
  std::printf("%s\n{\"sizes\":[", first ? "" : ",");
  first = false;
  for (int i = 0; i < n; i++) std::printf("%s[%d,%d]", i ? "," : "", sizes[i].w, sizes[i].h);
  std::printf("],\"layout\":[");
  for (size_t i = 0; i < layout.size(); i++) { auto &g = layout[i]; std::printf("%s[%d,%d,%d,%d,%d]", i ? "," : "", g.geometry.x(), g.geometry.y(), g.geometry.width(), g.geometry.height(), g.sides.v); }
  std::printf("]}");
 }
 std::printf("\n]\n");
}
