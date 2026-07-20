"""Крипта чанков — wire-совместима с webdav-tunnel (AES-256-GCM).

Раскладка зашифрованного чанка: [12 nonce][ciphertext][16 tag].
Ключ выводится из WebDAV-пароля с фиксированным доменным префиксом, чтобы не
пересекаться с другими применениями того же пароля.
"""

from __future__ import annotations

import hashlib
import os

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

_KEY_PREFIX = b"webdav-tunnel-v1:"
NONCE_LEN = 12
TAG_LEN = 16


def derive_key(password: str) -> bytes:
    """32-байтный ключ AES-256 из пароля (SHA-256 с доменным префиксом)."""
    return hashlib.sha256(_KEY_PREFIX + password.encode()).digest()


def encrypt_chunk(key: bytes, plaintext: bytes) -> bytes:
    """[12 nonce][ct][16 tag]. AESGCM.encrypt возвращает ct‖tag — совпадает с Go Seal."""
    nonce = os.urandom(NONCE_LEN)
    return nonce + AESGCM(key).encrypt(nonce, plaintext, None)


def decrypt_chunk(key: bytes, blob: bytes) -> bytes:
    """Обратно к encrypt_chunk. Бросает при слишком коротком/битом входе."""
    if len(blob) < NONCE_LEN + TAG_LEN:
        raise ValueError(f"ciphertext too short ({len(blob)} bytes)")
    nonce, ct = blob[:NONCE_LEN], blob[NONCE_LEN:]
    return AESGCM(key).decrypt(nonce, ct, None)
