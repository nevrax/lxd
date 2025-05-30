# In case a cached image matching the desired alias is present, that
# one is preferred, even if the its remote has a more recent one.
test_image_prefer_cached() {
  if lxc image alias list testimage | grep -wF "testimage"; then
      lxc image delete testimage
  fi

  local LXD2_DIR LXD2_ADDR
  LXD2_DIR=$(mktemp -d -p "${TEST_DIR}" XXX)
  spawn_lxd "${LXD2_DIR}" true
  LXD2_ADDR=$(cat "${LXD2_DIR}/lxd.addr")

  (LXD_DIR=${LXD2_DIR} deps/import-busybox --alias testimage --public)
  fp1="$(LXD_DIR=${LXD2_DIR} lxc image info testimage | awk '/^Fingerprint/ {print $2}')"

  token="$(LXD_DIR=${LXD2_DIR} lxc config trust add --name foo -q)"
  lxc remote add l2 "${LXD2_ADDR}" --token "${token}"
  lxc init l2:testimage c1

  # Now the first image image is in the local store, since it was
  # downloaded to create c1.
  alias="$(lxc image info "${fp1}" | awk '{if ($1 == "Alias:") {print $2}}')"
  [ "${alias}" = "testimage" ]

  # Delete the first image from the remote store and replace it with a
  # new one with a different fingerprint (passing "--template create"
  # will do that).
  (LXD_DIR=${LXD2_DIR} lxc image delete testimage)
  (LXD_DIR=${LXD2_DIR} deps/import-busybox --alias testimage --public --template create)
  fp2="$(LXD_DIR=${LXD2_DIR} lxc image info testimage | awk '/^Fingerprint/ {print $2}')"
  [ "${fp1}" != "${fp2}" ]

  # At this point starting a new container from "testimage" should not
  # result in the new image being downloaded.
  lxc init l2:testimage c2
  if lxc image info "${fp2}"; then
      echo "The second image ${fp2} was downloaded and the cached one not used"
      return 1
  fi

  lxc delete c1
  lxc delete c2
  lxc remote remove l2
  lxc image delete "${fp1}"

  kill_lxd "$LXD2_DIR"
}
