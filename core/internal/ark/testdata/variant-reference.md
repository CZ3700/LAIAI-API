# Fast/Mini reference-video smoke fixture

`variant-reference.mp4` is a project-generated synthetic animation: a teal
square moves across a pale background. It contains no people, user data, audio
or third-party media. It is used only for video-input integration tests.

- H.264, yuv420p, 640 × 640, 24 FPS, 96 frames, exactly 4 seconds.
- Size: 7,968 bytes.
- SHA-256: `63e9ec90e03a504b952e195ce36ad8cdb2891f1690f4a63f2b8c11900bbe84d2`.

For live acceptance, use the raw GitHub URL pinned to the **full commit SHA**,
not a mutable branch URL. Fetch those exact remote bytes, compare the digest,
measure with ffprobe, and register the measured media through the authenticated
administrative API with a short evidence expiry. The registration itself does
not upload a file or prove that a mutable external URL is immutable.

Generated with FFmpeg color sources and an overlay moving at 80 pixels/second,
encoded by libx264 at CRF 24 with metadata removed and MP4 fast-start enabled.
This fixture is available under the repository's license.
