package virthandler

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	k8sv1 "k8s.io/api/core/v1"

	v1 "kubevirt.io/api/core/v1"
	diskutils "kubevirt.io/kubevirt/pkg/ephemeral-disk-utils"
	hostdisk "kubevirt.io/kubevirt/pkg/host-disk"
	"kubevirt.io/kubevirt/pkg/util/types"
	"kubevirt.io/kubevirt/pkg/virt-handler/isolation"
	"kubevirt.io/kubevirt/pkg/virt-handler/selinux"
)

func changeOwnershipOfBlockDevices(vmi *v1.VirtualMachineInstance, res isolation.IsolationResult) error {
	volumeModes := map[string]*k8sv1.PersistentVolumeMode{}
	for _, volumeStatus := range vmi.Status.VolumeStatus {
		if volumeStatus.PersistentVolumeClaimInfo != nil {
			volumeModes[volumeStatus.Name] = volumeStatus.PersistentVolumeClaimInfo.VolumeMode
		}
	}

	for i := range vmi.Spec.Volumes {
		volume := vmi.Spec.Volumes[i]
		if volume.VolumeSource.PersistentVolumeClaim == nil {
			continue
		}

		volumeMode, exists := volumeModes[volume.Name]
		if !exists {
			return fmt.Errorf("missing volume status for volume %s", volume.Name)
		}

		if !types.IsPVCBlock(volumeMode) {
			continue
		}

		devPath := filepath.Join(string(filepath.Separator), "dev", volume.Name)
		if err := diskutils.DefaultOwnershipManager.SetFileOwnership(filepath.Join(res.MountRoot(), devPath)); err != nil {
			return err
		}

	}
	return nil
}

func changeOwnershipAndRelabel(path string) error {
	err := diskutils.DefaultOwnershipManager.SetFileOwnership(path)
	if err != nil {
		return err
	}

	seLinux, selinuxEnabled, err := selinux.NewSELinux()
	if err == nil && selinuxEnabled {
		unprivilegedContainerSELinuxLabel := "system_u:object_r:container_file_t:s0"
		err = selinux.RelabelFiles(unprivilegedContainerSELinuxLabel, seLinux.IsPermissive(), filepath.Join(path))
		if err != nil {
			return (fmt.Errorf("error relabeling %s: %v", path, err))
		}

	}
	return err
}

// changeOwnershipOfHostDisks needs unmodified vmi (not passed to ReplacePVCByHostDisk function)
func changeOwnershipOfHostDisks(vmiWithAllPVCs *v1.VirtualMachineInstance, res isolation.IsolationResult) error {
	for i := range vmiWithAllPVCs.Spec.Volumes {
		if volumeSource := &vmiWithAllPVCs.Spec.Volumes[i].VolumeSource; volumeSource.HostDisk != nil {
			volumeName := vmiWithAllPVCs.Spec.Volumes[i].Name
			diskPath := hostdisk.GetMountedHostDiskPath(volumeName, volumeSource.HostDisk.Path)

			_, err := os.Stat(diskPath)
			if err != nil {
				if os.IsNotExist(err) {
					diskDir := hostdisk.GetMountedHostDiskDir(volumeName)
					if err := changeOwnershipAndRelabel(filepath.Join(res.MountRoot(), diskDir)); err != nil {
						return fmt.Errorf("Failed to change ownership of HostDisk dir %s, %s", volumeName, err)
					}
					continue
				}
				return fmt.Errorf("Failed to recognize if hostdisk contains image, %s", err)
			}

			err = changeOwnershipAndRelabel(filepath.Join(res.MountRoot(), diskPath))
			if err != nil {
				return fmt.Errorf("Failed to change ownership of HostDisk image: %s", err)
			}

		}
	}
	return nil
}

func (d *VirtualMachineController) prepareStorage(vmi *v1.VirtualMachineInstance, res isolation.IsolationResult) error {
	if err := changeOwnershipOfBlockDevices(vmi, res); err != nil {
		return err
	}
	return changeOwnershipOfHostDisks(vmi, res)
}

func getTapDevices(vmi *v1.VirtualMachineInstance) []string {
	macvtap := map[string]bool{}
	for _, inf := range vmi.Spec.Domain.Devices.Interfaces {
		if inf.Macvtap != nil {
			macvtap[inf.Name] = true
		}
	}

	tapDevices := []string{}
	for _, net := range vmi.Spec.Networks {
		_, ok := macvtap[net.Name]
		if ok {
			if net.Multus != nil {
				tapDevices = append(tapDevices, net.Multus.NetworkName)
			} else if net.Kactus != nil {
				tapDevices = append(tapDevices, net.Kactus.NetworkName)
			}
		}
	}
	return tapDevices
}

func (d *VirtualMachineController) prepareTap(vmi *v1.VirtualMachineInstance, res isolation.IsolationResult) error {
	tapDevices := getTapDevices(vmi)
	for _, tap := range tapDevices {
		path := filepath.Join(res.MountRoot(), "sys", "class", "net", tap, "ifindex")
		b, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("Failed to read if index, %v", err)
		}

		index, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return err
		}

		pathToTap := filepath.Join(res.MountRoot(), "dev", fmt.Sprintf("tap%d", index))

		if err := diskutils.DefaultOwnershipManager.SetFileOwnership(pathToTap); err != nil {
			return err
		}
	}
	return nil

}

func (*VirtualMachineController) prepareVFIO(vmi *v1.VirtualMachineInstance, res isolation.IsolationResult) error {
	vfioPath := filepath.Join(res.MountRoot(), "dev", "vfio")
	err := os.Chmod(filepath.Join(vfioPath, "vfio"), 0666)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	groups, err := ioutil.ReadDir(vfioPath)
	if err != nil {
		return err
	}

	for _, group := range groups {
		if group.Name() == "vfio" {
			continue
		}
		if err := diskutils.DefaultOwnershipManager.SetFileOwnership(filepath.Join(vfioPath, group.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (d *VirtualMachineController) nonRootSetup(origVMI, vmi *v1.VirtualMachineInstance) error {
	res, err := d.podIsolationDetector.Detect(origVMI)
	if err != nil {
		return err
	}
	if err := d.prepareStorage(origVMI, res); err != nil {
		return err
	}
	if err := d.prepareTap(origVMI, res); err != nil {
		return err
	}
	if err := d.prepareVFIO(origVMI, res); err != nil {
		return err
	}
	return nil
}
