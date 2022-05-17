/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2022 Kaloom, Inc.
 *
 */

package services

import (
	"encoding/json"
	"fmt"

	v1 "kubevirt.io/api/core/v1"
)

type kactusNetworkAnnotation struct {
	NetworkName string `json:"name"`                // required parameter: the network name for the CRD network resource in k8s
	IfMAC       string `json:"ifMac,omitempty"`     // optional parameter: the network device mac address in the form of 00:11:22:33:44:55
	IsPrimary   bool   `json:"isPrimary,omitempty"` // optional parameter: specify that this network is associated with the primary device in the Pod i.e. eth0
	Namespace   string `json:"namespace,omitempty"` // optional parameter: the namespace to which this network belongs to, if not specified it would be the namespace of the pod
}

type kactusNetworkAnnotationPool struct {
	pool []kactusNetworkAnnotation
}

func (mnap *kactusNetworkAnnotationPool) add(kactusNetworkAnnotation kactusNetworkAnnotation) {
	mnap.pool = append(mnap.pool, kactusNetworkAnnotation)
}

func (mnap kactusNetworkAnnotationPool) isEmpty() bool {
	return len(mnap.pool) == 0
}

func (mnap kactusNetworkAnnotationPool) toString() (string, error) {
	kactusNetworksAnnotation, err := json.Marshal(mnap.pool)
	if err != nil {
		return "", fmt.Errorf("failed to create JSON list from kactus interface pool %v", mnap.pool)
	}
	return string(kactusNetworksAnnotation), nil
}

func generateKactusCNIAnnotation(vmi *v1.VirtualMachineInstance) (string, error) {
	kactusNetworkAnnotationPool := kactusNetworkAnnotationPool{}

	for _, network := range vmi.Spec.Networks {
		if network.Kactus != nil {
			kactusNetworkAnnotationPool.add(newKactusAnnotationData(vmi, network))
		}
	}

	if !kactusNetworkAnnotationPool.isEmpty() {
		return kactusNetworkAnnotationPool.toString()
	}
	return "", nil
}

func newKactusAnnotationData(vmi *v1.VirtualMachineInstance, network v1.Network) kactusNetworkAnnotation {
	kactusIface := getIfaceByName(vmi, network.Name)
	namespace, networkName := getNamespaceAndNetworkName(vmi, network.Kactus.NetworkName)
	var kactusIfaceMac string
	if kactusIface != nil {
		kactusIfaceMac = kactusIface.MacAddress
	}
	return kactusNetworkAnnotation{
		IfMAC:       kactusIfaceMac,
		NetworkName: networkName,
		IsPrimary:   network.Kactus.Default,
		Namespace:   namespace,
	}
}
