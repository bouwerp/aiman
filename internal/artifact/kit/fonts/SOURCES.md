# Font sources

Latin subset woff2 files, unmodified, from the `@fontsource` packages on the npm
registry (all version 5.3.0, licence OFL-1.1, see OFL.txt). Each package tarball
was checked against the registry's `dist.integrity` (sha512) before unpacking;
each file is `package/files/<name>` inside it.

These files are not published. `font-face.sh` inlines them into `../theme.css` as
`data:` URIs, because a sandboxed artifact page (origin `null`) cannot load
same-host fonts: Chromium fetches fonts in CORS mode. CI runs `font-face.sh --check`.

| Package | Tarball integrity | File | sha256 |
|---|---|---|---|
| `@fontsource/ibm-plex-mono@5.3.0` | `sha512-eTgnZjZEGk1QtD3ZstF+Vclo2HLAni8YMy34/DxllwZvyz1lR/1RF/xTiAquOBO7MvqBx8D2Ig2WCPMVfdZu7Q==` | `ibm-plex-mono-latin-400-normal.woff2` | `08949f728dc52d528e69b1667d15c89a5686a4ee9a296ff90983985f99c380f7` |
| `@fontsource/ibm-plex-mono@5.3.0` | `sha512-eTgnZjZEGk1QtD3ZstF+Vclo2HLAni8YMy34/DxllwZvyz1lR/1RF/xTiAquOBO7MvqBx8D2Ig2WCPMVfdZu7Q==` | `ibm-plex-mono-latin-500-normal.woff2` | `01d285447409c8a588692162439a038b8cbd7871309ee20267b0d2d91c6e8e22` |
| `@fontsource/ibm-plex-sans@5.3.0` | `sha512-CbE4CbbEEZJX860XyUiRpsksXIQR8Rp2XDva2VO53NJox9tVNtusrysd2x5YkUEY3ErQ66W1IiiQL8/wihhw5w==` | `ibm-plex-sans-latin-400-normal.woff2` | `3b646991d30055a93a4ecc499713d4347953a74a947ecab435ab72070cbdab0e` |
| `@fontsource/ibm-plex-sans@5.3.0` | `sha512-CbE4CbbEEZJX860XyUiRpsksXIQR8Rp2XDva2VO53NJox9tVNtusrysd2x5YkUEY3ErQ66W1IiiQL8/wihhw5w==` | `ibm-plex-sans-latin-500-normal.woff2` | `0717336fb31fcdcde4b8deb3675bb4a0f7f6d484864afcd6751ac29975962203` |
| `@fontsource/ibm-plex-sans@5.3.0` | `sha512-CbE4CbbEEZJX860XyUiRpsksXIQR8Rp2XDva2VO53NJox9tVNtusrysd2x5YkUEY3ErQ66W1IiiQL8/wihhw5w==` | `ibm-plex-sans-latin-600-normal.woff2` | `8960851d691c054ed38e259bdcf1a6190d157b4203ed5bb32c632a863fb8ec2f` |
| `@fontsource/inter@5.3.0` | `sha512-RofMylZmjlJEfELXeNHFWBRcSs75rGU/6bV2S2jfnvv/3rPXPGe0LgUJTklcHZ9lM4OZmAVFhcJPnACfb91A3g==` | `inter-latin-400-normal.woff2` | `8909904ab6c872eb994093482a88a28eca2cd95912d7b6fecd72103b0dc07edc` |
| `@fontsource/inter@5.3.0` | `sha512-RofMylZmjlJEfELXeNHFWBRcSs75rGU/6bV2S2jfnvv/3rPXPGe0LgUJTklcHZ9lM4OZmAVFhcJPnACfb91A3g==` | `inter-latin-500-normal.woff2` | `f3779f1efccc4bdcdf9c0a02ab95bf6bd092ed09c48c08cedc725889edd1d19f` |
| `@fontsource/inter@5.3.0` | `sha512-RofMylZmjlJEfELXeNHFWBRcSs75rGU/6bV2S2jfnvv/3rPXPGe0LgUJTklcHZ9lM4OZmAVFhcJPnACfb91A3g==` | `inter-latin-600-normal.woff2` | `f9a06e79cd3a2a20951c0f0e28f66dd0e6d3fda73911d640a2125c8fcb78f21a` |
| `@fontsource/jetbrains-mono@5.3.0` | `sha512-fqDfB5I9f1p1TV486aUgB9t8zP84P0O1FtQR5Ol9vjwPy+S+EIGlVYm1cvj2W5shcZMTg2nZFdVMoH5wFu8a1A==` | `jetbrains-mono-latin-400-normal.woff2` | `14425ba9c695763c1547f48a206b7aa60350a33ae23de09f0407877f3fcd89eb` |
| `@fontsource/jetbrains-mono@5.3.0` | `sha512-fqDfB5I9f1p1TV486aUgB9t8zP84P0O1FtQR5Ol9vjwPy+S+EIGlVYm1cvj2W5shcZMTg2nZFdVMoH5wFu8a1A==` | `jetbrains-mono-latin-500-normal.woff2` | `cb182feeed4d798ff6961d3c79f7026279448fca0676438aaecb21f3fc39553a` |
| `@fontsource/space-grotesk@5.3.0` | `sha512-ksnGizDPXIDuvqcTYTSrmZ+evx9sDlS8rp7+42BQ7wU+spt3twEoXfbJz672C+5CLg6VeUQwRy5RXshWb67LcQ==` | `space-grotesk-latin-500-normal.woff2` | `1b1a8131d9edf975d9decee81e2f2bf504812f7a4f498e5500f28a613e22e64c` |
| `@fontsource/space-grotesk@5.3.0` | `sha512-ksnGizDPXIDuvqcTYTSrmZ+evx9sDlS8rp7+42BQ7wU+spt3twEoXfbJz672C+5CLg6VeUQwRy5RXshWb67LcQ==` | `space-grotesk-latin-600-normal.woff2` | `685bbbf69fa616df1ef81847c85fc76be097ddfb3468ff2257be54511ab3130f` |
| `@fontsource/space-grotesk@5.3.0` | `sha512-ksnGizDPXIDuvqcTYTSrmZ+evx9sDlS8rp7+42BQ7wU+spt3twEoXfbJz672C+5CLg6VeUQwRy5RXshWb67LcQ==` | `space-grotesk-latin-700-normal.woff2` | `35f8aec56cfd5cbfdb03cc68733a54a0b05bb3617ffcd5fd332badc0b045ca55` |
