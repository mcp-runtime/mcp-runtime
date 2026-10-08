import struct
import unittest

from image_architecture import validate_elf


def header(machine: int, byte_order: int = 1) -> bytes:
    data = bytearray(64)
    data[:6] = b"\x7fELF\x02" + bytes([byte_order])
    struct.pack_into("<H" if byte_order == 1 else ">H", data, 18, machine)
    return bytes(data)


class ImageArchitectureTests(unittest.TestCase):
    def test_native_binaries(self):
        validate_elf(header(183), "arm64")
        validate_elf(header(62), "amd64")
        validate_elf(header(183, 2), "arm64")

    def test_rejects_amd64_executable_in_arm64_image(self):
        with self.assertRaisesRegex(ValueError, "does not match arm64"):
            validate_elf(header(62), "arm64")

    def test_rejects_arm64_executable_in_amd64_image(self):
        with self.assertRaisesRegex(ValueError, "does not match amd64"):
            validate_elf(header(183), "amd64")

    def test_rejects_invalid_executable(self):
        for data in [b"", b"not an ELF binary", b"\x7fELF\x01" + bytes(60), b"\x7fELF\x02\x00" + bytes(58)]:
            with self.subTest(data=data[:6]), self.assertRaises(ValueError):
                validate_elf(data, "arm64")


if __name__ == "__main__":
    unittest.main()
