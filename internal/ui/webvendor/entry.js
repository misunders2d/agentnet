// The only parts of age-encryption (typage) the browser device uses: X25519
// encryption to a recipient string and decryption with a WebCrypto key.
export { Encrypter, Decrypter, identityToRecipient } from "age-encryption";
