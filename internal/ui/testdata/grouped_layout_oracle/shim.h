// Shim for the oracle only: the minimum of Qt, range-v3, rpl, gsl and base
// that Telegram Desktop's unmodified ui/grouped_layout.cpp uses.
#pragma once
#include <vector>
#include <string>
#include <algorithm>
#include <numeric>
#include <cmath>
#include <span>
#include <stdexcept>
using float64 = double;
struct QSize { int w = 0, h = 0; QSize() = default; QSize(int a, int b) : w(a), h(b) {} int width() const { return w; } int height() const { return h; } };
struct QRect { int x_ = 0, y_ = 0, w_ = 0, h_ = 0; QRect() = default; QRect(int x, int y, int w, int h) : x_(x), y_(y), w_(w), h_(h) {}
 QRect(double x, double y, int w, int h) : x_(int(x)), y_(int(y)), w_(w), h_(h) {}
 QRect(int x, double y, int w, int h) : x_(x), y_(int(y)), w_(w), h_(h) {}
 int x() const { return x_; } int y() const { return y_; } int width() const { return w_; } int height() const { return h_; } };
enum class RectPart { None = 0, TopLeft = 1, Top = 2, TopRight = 4, Left = 8, Right = 16, BottomLeft = 32, Bottom = 64, BottomRight = 128 };
struct RectParts { int v = 0; RectParts() = default; RectParts(RectPart p) : v(int(p)) {} explicit RectParts(int x) : v(x) {} explicit operator bool() const { return v != 0; } };
inline RectParts operator|(RectParts a, RectParts b) { return RectParts(a.v | b.v); }
inline RectParts operator|(RectPart a, RectPart b) { return RectParts(int(a) | int(b)); }
inline RectParts operator|(RectParts a, RectPart b) { return RectParts(a.v | int(b)); }
inline RectParts operator&(RectParts a, RectPart b) { return RectParts(a.v & int(b)); }
#define Expects(x) do { if (!(x)) throw std::logic_error(#x); } while (0)
#define Assert(x) Expects(x)
namespace base { inline double SafeRound(double v) { return std::round(v); } }
namespace gsl { template <typename T> std::span<const T> make_span(const std::vector<T> &v) { return std::span<const T>(v); } }
namespace rpl::mappers { struct P1 {}; inline P1 _1; struct Gt { double v; bool operator()(double x) const { return x > v; } }; inline Gt operator>(P1, double v) { return {v}; } }
namespace ranges {
 template <typename C> struct All { const C &c; };
 namespace views {
  template <typename C> All<C> all(const C &c) { return {c}; }
  template <typename F> struct Tr { F f; };
  template <typename F> Tr<F> transform(F f) { return {f}; }
 }
 template <typename C, typename F> auto operator|(All<C> a, views::Tr<F> t) { using R = decltype(t.f(*a.c.begin())); std::vector<R> out; for (const auto &x : a.c) out.push_back(t.f(x)); return out; }
 struct ToVector {}; inline ToVector to_vector;
 template <typename T> std::vector<T> operator|(std::vector<T> v, ToVector) { return v; }
 template <typename Out> struct To {};
 template <typename Out> To<Out> to() { return {}; }
 inline std::string operator|(std::vector<char> v, To<std::string>) { return std::string(v.begin(), v.end()); }
 template <typename C, typename T> T accumulate(const C &c, T init) { for (const auto &x : c) init = init + x; return init; }
 template <typename C, typename P> auto find_if(const C &c, P p) { return std::find_if(c.begin(), c.end(), p); }
 template <typename C> auto min(const C &c) { return *std::min_element(c.begin(), c.end()); }
}
