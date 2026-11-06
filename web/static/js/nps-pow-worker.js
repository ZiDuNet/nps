'use strict';

// Each login challenge hashes a short ASCII message:
// nonce + "\0" + decimal solution. The message always fits in one SHA-256
// block, so this worker specializes the compression loop and avoids creating
// one Promise-backed Web Crypto operation for every candidate.
var SHA256_K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
]);

var SHA256_H0 = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
]);

function rotateRight(value, bits) {
    return (value >>> bits) | (value << (32 - bits));
}

function sha256SingleBlock(block, words, state) {
    var i;
    for (i = 0; i < 16; i++) {
        var offset = i * 4;
        words[i] = ((block[offset] << 24) |
            (block[offset + 1] << 16) |
            (block[offset + 2] << 8) |
            block[offset + 3]) | 0;
    }
    for (i = 16; i < 64; i++) {
        var w15 = words[i - 15];
        var w2 = words[i - 2];
        var s0 = rotateRight(w15, 7) ^ rotateRight(w15, 18) ^ (w15 >>> 3);
        var s1 = rotateRight(w2, 17) ^ rotateRight(w2, 19) ^ (w2 >>> 10);
        words[i] = (words[i - 16] + s0 + words[i - 7] + s1) | 0;
    }

    var a = SHA256_H0[0] | 0;
    var b = SHA256_H0[1] | 0;
    var c = SHA256_H0[2] | 0;
    var d = SHA256_H0[3] | 0;
    var e = SHA256_H0[4] | 0;
    var f = SHA256_H0[5] | 0;
    var g = SHA256_H0[6] | 0;
    var h = SHA256_H0[7] | 0;

    for (i = 0; i < 64; i++) {
        var sigma1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
        var choose = (e & f) ^ (~e & g);
        var temp1 = (h + sigma1 + choose + SHA256_K[i] + words[i]) | 0;
        var sigma0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
        var majority = (a & b) ^ (a & c) ^ (b & c);
        var temp2 = (sigma0 + majority) | 0;
        h = g;
        g = f;
        f = e;
        e = (d + temp1) | 0;
        d = c;
        c = b;
        b = a;
        a = (temp1 + temp2) | 0;
    }

    state[0] = (SHA256_H0[0] + a) | 0;
    state[1] = (SHA256_H0[1] + b) | 0;
    state[2] = (SHA256_H0[2] + c) | 0;
    state[3] = (SHA256_H0[3] + d) | 0;
    state[4] = (SHA256_H0[4] + e) | 0;
    state[5] = (SHA256_H0[5] + f) | 0;
    state[6] = (SHA256_H0[6] + g) | 0;
    state[7] = (SHA256_H0[7] + h) | 0;
}

function passesDifficulty(state, bits) {
    var remaining = bits;
    for (var i = 0; i < state.length && remaining > 0; i++) {
        for (var shift = 24; shift >= 0 && remaining > 0; shift -= 8) {
            var value = (state[i] >>> shift) & 0xff;
            if (remaining >= 8) {
                if (value !== 0) return false;
                remaining -= 8;
                continue;
            }
            return (value & (0xff << (8 - remaining))) === 0;
        }
    }
    return true;
}

function runSelfTest() {
    var block = new Uint8Array(64);
    var words = new Int32Array(64);
    var state = new Int32Array(8);
    block[0] = 0x61;
    block[1] = 0x62;
    block[2] = 0x63;
    block[3] = 0x80;
    block[62] = 0;
    block[63] = 24;
    sha256SingleBlock(block, words, state);
    var expected = new Int32Array([
        0xba7816bf, 0x8f01cfea, 0x414140de, 0x5dae2223,
        0xb00361a3, 0x96177a9c, 0xb410ff61, 0xf20015ad
    ]);
    for (var i = 0; i < expected.length; i++) {
        if ((state[i] | 0) !== (expected[i] | 0)) {
            throw new Error('SHA-256 self-test failed');
        }
    }
}

function solveChallenge(nonce, bits) {
    if (!/^[0-9a-f]+$/i.test(nonce) || bits < 1 || bits > 248) {
        throw new Error('invalid PoW challenge');
    }
    var block = new Uint8Array(64);
    var words = new Int32Array(64);
    var state = new Int32Array(8);
    var nonceLength = nonce.length;
    var solutionOffset = nonceLength + 1;
    var startedAt = performance.now();
    var solution = 0;
    var attempts = 0;
    for (var i = 0; i < nonceLength; i++) block[i] = nonce.charCodeAt(i);
    block[nonceLength] = 0;

    while (true) {
        var value = solution;
        var digits = 1;
        var scale = 1;
        while (value >= scale * 10) {
            scale *= 10;
            digits++;
        }
        for (var position = solutionOffset + digits - 1; position >= solutionOffset; position--) {
            block[position] = 48 + (solution % 10);
            solution = Math.floor(solution / 10);
        }
        solution = value;
        var messageLength = solutionOffset + digits;
        block[messageLength] = 0x80;
        var bitLength = messageLength * 8;
        block[62] = (bitLength >>> 8) & 0xff;
        block[63] = bitLength & 0xff;

        sha256SingleBlock(block, words, state);
        attempts++;
        if (passesDifficulty(state, bits)) {
            return {
                solution: String(solution),
                attempts: attempts,
                elapsedMs: Math.round(performance.now() - startedAt)
            };
        }
        block[messageLength] = 0;
        solution++;
        if ((attempts & 0xffff) === 0) {
            self.postMessage({type: 'progress', attempts: attempts, elapsedMs: Math.round(performance.now() - startedAt)});
            if (performance.now() - startedAt > 110000) {
                throw new Error('PoW challenge calculation timed out');
            }
        }
    }
}

runSelfTest();

self.onmessage = function (event) {
    var data = event.data || {};
    if (data.type !== 'solve') return;
    try {
        var result = solveChallenge(String(data.nonce || ''), Number(data.bits || 0));
        self.postMessage({
            type: 'result',
            solution: result.solution,
            attempts: result.attempts,
            elapsedMs: result.elapsedMs
        });
    } catch (error) {
        self.postMessage({type: 'error', message: error && error.message ? error.message : 'PoW failed'});
    }
};
