# Telegram album layout oracle (MEL-856)

`grouped_layout_vectors.json` is the output of Telegram Desktop's unmodified
`Telegram/SourceFiles/ui/grouped_layout.cpp` and `grouped_layout.h` at
`fca0d08b6852df2195e11db91f7a4e3be2cbc4a4`, compiled against `shim.h` (the
few Qt, range-v3, rpl, gsl and base pieces the file uses) with the album sizes
of `history_view_media_grouped.cpp` (430, 100, 4):

    base=https://raw.githubusercontent.com/telegramdesktop/tdesktop/fca0d08b6852df2195e11db91f7a4e3be2cbc4a4/Telegram/SourceFiles/ui
    mkdir -p ui && cp shim.h ui/rect_part.h
    curl -fsSL -o ui/grouped_layout.cpp $base/grouped_layout.cpp
    curl -fsSL -o ui/grouped_layout.h $base/grouped_layout.h
    g++ -std=c++20 -I. -include ui/rect_part.h main.cpp ui/grouped_layout.cpp -o oracle
    ./oracle < cases.txt   # one case per line: count, then width height pairs

Each layout entry is `[x, y, width, height, sides]` with RectPart bits
(Top 2, Left 8, Right 16, Bottom 64). The Telegram source is not vendored.
